/* ─────────────────────────────────────────────────────────────────────────────
   Conduit Dashboard — app.js
   All API calls go to the same origin (Gateway serves both the UI and the API).
───────────────────────────────────────────────────────────────────────────── */

const API = '';   // Same origin — no base URL prefix needed

// ─── Connector icons & colours ───────────────────────────────────────────────
const CONNECTOR_META = {
  slack:  { icon: '💬', color: '#e01e5a' },
  github: { icon: '🐙', color: '#6e5494' },
  stripe: { icon: '💳', color: '#635bff' },
};

function connMeta(id) {
  return CONNECTOR_META[id] || { icon: '🔌', color: '#6366f1' };
}

// ─── Navigation ──────────────────────────────────────────────────────────────
const VIEW_TITLES = {
  catalog:   'Integration Catalog',
  instances: 'Installed Instances',
  execute:   'Execute Endpoint',
  metrics:   'Observability',
};

document.querySelectorAll('.nav-item').forEach(link => {
  link.addEventListener('click', e => {
    e.preventDefault();
    switchView(link.dataset.view);
  });
});

function switchView(name) {
  document.querySelectorAll('.nav-item').forEach(l => l.classList.remove('active'));
  document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
  document.getElementById('nav-' + name)?.classList.add('active');
  document.getElementById('view-' + name)?.classList.add('active');
  document.getElementById('view-title').textContent = VIEW_TITLES[name] || name;

  if (name === 'catalog')   loadCatalog();
  if (name === 'instances') loadInstances();
  if (name === 'execute')   loadExecuteSelects();
  if (name === 'metrics')   loadMetricsSummary();
}

// ─── Catalog ─────────────────────────────────────────────────────────────────
async function loadCatalog() {
  const grid = document.getElementById('catalog-grid');
  try {
    const connectors = await apiFetch('/v1/connectors');
    grid.innerHTML = '';

    if (!connectors || connectors.length === 0) {
      grid.innerHTML = '<div class="empty-state">No connectors registered.</div>';
      return;
    }

    for (const conn of connectors) {
      grid.appendChild(buildConnectorCard(conn));
    }
  } catch (err) {
    grid.innerHTML = `<div class="empty-state">Failed to load catalog: ${err.message}</div>`;
  }
}

function buildConnectorCard(conn) {
  const meta = connMeta(conn.id);
  const card = el('div', 'connector-card');

  const scopes = (conn.scopes || []).slice(0, 4)
    .map(s => `<span class="scope-pill">${s}</span>`)
    .join('');

  card.innerHTML = `
    <div class="card-header">
      <span class="card-icon">${meta.icon}</span>
      <span class="card-category">${conn.category || 'Integration'}</span>
    </div>
    <div class="card-name">${conn.name}</div>
    <div class="card-desc">${conn.description || ''}</div>
    ${scopes ? `<div class="card-scopes">${scopes}</div>` : ''}
  `;

  const installBtn = el('button', 'btn btn-primary btn-sm');
  installBtn.textContent = '⚡ Install';
  installBtn.onclick = () => openInstallModal(conn);
  card.appendChild(installBtn);

  return card;
}

// ─── Install Modal ────────────────────────────────────────────────────────────
let _installing = null;

function openInstallModal(conn) {
  _installing = conn;
  const meta = connMeta(conn.id);
  const modal = document.getElementById('install-modal');
  const body  = document.getElementById('modal-body');
  document.getElementById('modal-title').textContent = `Install ${conn.name}`;

  const isAPIKey = conn.scopes?.length === 0 || conn.id === 'stripe';

  if (isAPIKey) {
    body.innerHTML = `
      <p>${meta.icon} <strong>${conn.name}</strong> uses an <strong>API key</strong> for authentication.</p>
      <div class="form-group">
        <label for="modal-api-key">API Key</label>
        <input id="modal-api-key" class="form-input" type="password" placeholder="sk_live_..." autocomplete="off" />
      </div>
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick="closeInstallModal()">Cancel</button>
        <button class="btn btn-primary" onclick="installAPIKey('${conn.id}')">Install</button>
      </div>
    `;
  } else {
    body.innerHTML = `
      <p>${meta.icon} <strong>${conn.name}</strong> uses <strong>OAuth 2.0</strong>. You'll be redirected to the provider to authorise access.</p>
      <p>Requested scopes: <strong>${(conn.scopes || []).join(', ')}</strong></p>
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick="closeInstallModal()">Cancel</button>
        <button class="btn btn-primary" onclick="installOAuth('${conn.id}')">Authorise &rarr;</button>
      </div>
    `;
  }

  modal.style.display = 'flex';
}

function closeInstallModal() {
  document.getElementById('install-modal').style.display = 'none';
  _installing = null;
}

// Close modal on backdrop click
document.getElementById('install-modal').addEventListener('click', e => {
  if (e.target === e.currentTarget) closeInstallModal();
});

async function installOAuth(connectorID) {
  try {
    const result = await apiFetch(`/v1/connectors/${connectorID}/install`, { method: 'POST' });
    closeInstallModal();
    if (result.redirect_url) {
      // Open OAuth in same tab — the gateway returns an HTML success page
      window.location.href = result.redirect_url;
    }
  } catch (err) {
    toast('Install failed: ' + err.message, 'error');
  }
}

async function installAPIKey(connectorID) {
  const key = document.getElementById('modal-api-key')?.value?.trim();
  if (!key) {
    toast('API key is required', 'error');
    return;
  }
  try {
    const result = await apiFetch(`/v1/connectors/${connectorID}/install`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ api_key: key }),
    });
    closeInstallModal();
    toast(`${connectorID} installed! Instance: ${result.instance_id}`, 'success');
    switchView('instances');
  } catch (err) {
    toast('Install failed: ' + err.message, 'error');
  }
}

// ─── Instances ────────────────────────────────────────────────────────────────
async function loadInstances() {
  const list = document.getElementById('instances-list');
  list.innerHTML = '<div class="empty-state" style="opacity:0.5">Loading…</div>';
  try {
    const instances = await apiFetch('/v1/instances');
    updateBadge(instances?.length || 0);
    document.getElementById('metric-instances-count').textContent = instances?.length ?? 0;

    if (!instances || instances.length === 0) {
      list.innerHTML = '<div class="empty-state">No instances installed yet. Install a connector from the Catalog.</div>';
      return;
    }

    list.innerHTML = '';
    for (const inst of instances) {
      list.appendChild(buildInstanceRow(inst));
    }
  } catch (err) {
    list.innerHTML = `<div class="empty-state">Failed to load instances: ${err.message}</div>`;
  }
}

function buildInstanceRow(inst) {
  const meta = connMeta(inst.connector_id);
  const statusClass = `status-${inst.status}`;

  const row = el('div', 'instance-row');
  row.innerHTML = `
    <span style="font-size:1.5rem">${meta.icon}</span>
    <div class="instance-info">
      <div class="instance-connector">${inst.connector_id}</div>
      <div class="instance-id">${inst.id}</div>
    </div>
    <span class="status-badge ${statusClass}">${inst.status}</span>
    <div class="instance-actions">
      <button class="btn btn-secondary btn-sm" onclick="quickExecute('${inst.id}', '${inst.connector_id}')">▶ Execute</button>
      <button class="btn btn-danger btn-sm" onclick="deleteInstance('${inst.id}')">✕ Remove</button>
    </div>
  `;
  return row;
}

function quickExecute(instanceID, connectorID) {
  switchView('execute');
  setTimeout(() => {
    const sel = document.getElementById('exec-instance');
    if (sel) {
      sel.value = instanceID;
      sel.dispatchEvent(new Event('change'));
    }
  }, 100);
}

async function deleteInstance(id) {
  if (!confirm(`Remove instance ${id}?`)) return;
  try {
    await apiFetch(`/v1/instances/${id}`, { method: 'DELETE' });
    toast('Instance removed', 'success');
    loadInstances();
  } catch (err) {
    toast('Failed to remove: ' + err.message, 'error');
  }
}

function updateBadge(count) {
  const badge = document.getElementById('instances-badge');
  badge.textContent = count;
  badge.style.display = count > 0 ? '' : 'none';
}

// ─── Execute Panel ────────────────────────────────────────────────────────────

// Endpoint metadata per connector
const ENDPOINTS = {
  slack:  ['send_message'],
  github: ['create_issue', 'list_issues'],
  stripe: ['list_charges'],
};

// Default bodies for each endpoint
const DEFAULT_BODIES = {
  send_message:  JSON.stringify({ channel: 'C_GENERAL', text: 'Hello from Conduit!' }, null, 2),
  create_issue:  JSON.stringify({ owner: 'my-org', repo: 'my-repo', title: 'New issue from Conduit', body: 'Auto-created.' }, null, 2),
  list_issues:   JSON.stringify({}, null, 2),
  list_charges:  JSON.stringify({}, null, 2),
};

async function loadExecuteSelects() {
  const instSel = document.getElementById('exec-instance');
  const epSel   = document.getElementById('exec-endpoint');

  try {
    const instances = await apiFetch('/v1/instances');
    instSel.innerHTML = instances?.length
      ? instances.map(i => `<option value="${i.id}" data-connector="${i.connector_id}">${i.connector_id} — ${i.id}</option>`).join('')
      : '<option value="">No instances available</option>';

    instSel.onchange = () => {
      const opt = instSel.selectedOptions[0];
      const connID = opt?.dataset.connector || '';
      const eps = ENDPOINTS[connID] || [];
      epSel.innerHTML = eps.map(e => `<option value="${e}">${e}</option>`).join('');
      epSel.dispatchEvent(new Event('change'));
    };

    epSel.onchange = () => {
      const ep = epSel.value;
      if (DEFAULT_BODIES[ep]) {
        document.getElementById('exec-body').value = DEFAULT_BODIES[ep];
      }
    };

    instSel.dispatchEvent(new Event('change'));
  } catch (err) {
    instSel.innerHTML = '<option>Error loading instances</option>';
  }
}

async function executeEndpoint() {
  const instanceID   = document.getElementById('exec-instance').value;
  const endpointName = document.getElementById('exec-endpoint').value;
  const bodyText     = document.getElementById('exec-body').value.trim();

  if (!instanceID || !endpointName) {
    toast('Select an instance and endpoint first', 'error');
    return;
  }

  const btn = document.getElementById('exec-btn');
  btn.textContent = '⏳ Executing…';
  btn.disabled = true;

  const responseBox    = document.getElementById('response-box');
  const responseStatus = document.getElementById('response-status');
  const responseBody   = document.getElementById('response-body');

  try {
    const url = `/v1/instances/${instanceID}/endpoints/${endpointName}`;
    const raw = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: bodyText || '{}',
    });

    const text = await raw.text();
    let formatted;
    try {
      formatted = JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      formatted = text;
    }

    responseStatus.textContent = `${raw.status} ${raw.statusText}`;
    responseStatus.className = 'response-status ' + (raw.ok ? 'ok' : 'err');
    responseBody.textContent = formatted;
    responseBox.style.display = '';

    if (raw.ok) {
      toast(`${endpointName} executed successfully (${raw.status})`, 'success');
    } else {
      toast(`Endpoint returned ${raw.status}`, 'error');
    }
  } catch (err) {
    toast('Request failed: ' + err.message, 'error');
  } finally {
    btn.textContent = '▶ Execute';
    btn.disabled = false;
  }
}

// ─── Metrics Summary ──────────────────────────────────────────────────────────
async function loadMetricsSummary() {
  try {
    const instances = await apiFetch('/v1/instances');
    document.getElementById('metric-instances-count').textContent = instances?.length ?? 0;
  } catch {}
}

// ─── Utilities ────────────────────────────────────────────────────────────────
function el(tag, className) {
  const e = document.createElement(tag);
  if (className) e.className = className;
  return e;
}

async function apiFetch(path, options = {}) {
  const resp = await fetch(API + path, options);
  const text = await resp.text();
  let data;
  try { data = JSON.parse(text); } catch { data = text; }
  if (!resp.ok) {
    const msg = (typeof data === 'object' && data?.error) ? data.error : String(data);
    throw new Error(msg || `HTTP ${resp.status}`);
  }
  return data;
}

function toast(message, type = 'info') {
  const container = document.getElementById('toast-container');
  const t = el('div', `toast toast-${type}`);
  const icon = type === 'success' ? '✅' : type === 'error' ? '❌' : 'ℹ️';
  t.innerHTML = `<span>${icon}</span> <span>${message}</span>`;
  container.appendChild(t);
  setTimeout(() => t.remove(), 4000);
}

// ─── Init ─────────────────────────────────────────────────────────────────────
(async function init() {
  // Load catalog by default
  loadCatalog();

  // Warm up the badge
  try {
    const instances = await apiFetch('/v1/instances');
    updateBadge(instances?.length || 0);
  } catch {}
})();
