package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"conduit/sdk"

	_ "modernc.org/sqlite"
)

type DB struct {
	db            *sql.DB
	encryptionKey []byte
}

type ConnectorInstance struct {
	ID             string    `json:"id"`
	ConnectorID    string    `json:"connector_id"`
	Status         string    `json:"status"` // "active", "drifted", "error"
	BaselineSchema string    `json:"baseline_schema"`
	WebhookSecret  string    `json:"webhook_secret"`
	CreatedAt      time.Time `json:"created_at"`
}

// NewDB opens or creates the SQLite database and executes migrations.
func NewDB(dbPath string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	db := &DB{
		db:            sqlDB,
		encryptionKey: getEncryptionKey(),
	}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

func (db *DB) Close() error {
	return db.db.Close()
}

func (db *DB) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS connector_instances (
			id TEXT PRIMARY KEY,
			connector_id TEXT NOT NULL,
			status TEXT NOT NULL,
			baseline_schema TEXT,
			webhook_secret TEXT,
			created_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS credentials (
			instance_id TEXT PRIMARY KEY,
			auth_type TEXT NOT NULL,
			encrypted_access_token TEXT,
			encrypted_refresh_token TEXT,
			token_expiry DATETIME,
			encrypted_api_key TEXT,
			FOREIGN KEY(instance_id) REFERENCES connector_instances(id) ON DELETE CASCADE
		);`,
	}

	for _, q := range queries {
		if _, err := db.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func getEncryptionKey() []byte {
	keyStr := os.Getenv("CONDUIT_ENCRYPTION_KEY")
	isProd := os.Getenv("CONDUIT_ENV") == "production"

	if keyStr == "" {
		if isProd {
			log.Fatalf("FATAL: CONDUIT_ENCRYPTION_KEY environment variable is not set in production!")
		} else {
			log.Println("WARNING: CONDUIT_ENCRYPTION_KEY is not set. Falling back to default development key.")
			keyStr = "dev_encryption_key_32_bytes_long"
		}
	}

	key := []byte(keyStr)
	if len(key) != 32 {
		if isProd {
			log.Fatalf("FATAL: CONDUIT_ENCRYPTION_KEY must be exactly 32 bytes, got %d bytes", len(key))
		} else {
			log.Printf("WARNING: CONDUIT_ENCRYPTION_KEY is %d bytes, padding/truncating to 32 bytes for development.", len(key))
			padded := make([]byte, 32)
			copy(padded, key)
			key = padded
		}
	}
	return key
}

func (db *DB) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(db.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (db *DB) decrypt(ciphertextStr string) (string, error) {
	if ciphertextStr == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextStr)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(db.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// Instance CRUD Operations

func (db *DB) SaveInstance(inst *ConnectorInstance) error {
	query := `INSERT INTO connector_instances (id, connector_id, status, baseline_schema, webhook_secret, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			baseline_schema = excluded.baseline_schema,
			webhook_secret = excluded.webhook_secret;`
	
	_, err := db.db.Exec(query, inst.ID, inst.ConnectorID, inst.Status, inst.BaselineSchema, inst.WebhookSecret, inst.CreatedAt)
	return err
}

func (db *DB) GetInstance(id string) (*ConnectorInstance, error) {
	query := `SELECT id, connector_id, status, baseline_schema, webhook_secret, created_at FROM connector_instances WHERE id = ?;`
	row := db.db.QueryRow(query, id)

	var inst ConnectorInstance
	err := row.Scan(&inst.ID, &inst.ConnectorID, &inst.Status, &inst.BaselineSchema, &inst.WebhookSecret, &inst.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("instance not found: %s", id)
		}
		return nil, err
	}
	return &inst, nil
}

func (db *DB) ListInstances() ([]*ConnectorInstance, error) {
	query := `SELECT id, connector_id, status, baseline_schema, webhook_secret, created_at FROM connector_instances ORDER BY created_at DESC;`
	rows, err := db.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*ConnectorInstance
	for rows.Next() {
		var inst ConnectorInstance
		if err := rows.Scan(&inst.ID, &inst.ConnectorID, &inst.Status, &inst.BaselineSchema, &inst.WebhookSecret, &inst.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, &inst)
	}
	return list, nil
}

func (db *DB) DeleteInstance(id string) error {
	query := `DELETE FROM connector_instances WHERE id = ?;`
	_, err := db.db.Exec(query, id)
	return err
}

// Credentials Operations

func (db *DB) SaveCredentials(instanceID string, authType sdk.AuthType, creds *sdk.Credentials) error {
	var encToken, encRefresh, encAPIKey string
	var err error

	if creds.Token != nil {
		encToken, err = db.encrypt(creds.Token.AccessToken)
		if err != nil {
			return fmt.Errorf("failed to encrypt access token: %w", err)
		}
		encRefresh, err = db.encrypt(creds.Token.RefreshToken)
		if err != nil {
			return fmt.Errorf("failed to encrypt refresh token: %w", err)
		}
	}

	if creds.APIKey != "" {
		encAPIKey, err = db.encrypt(creds.APIKey)
		if err != nil {
			return fmt.Errorf("failed to encrypt api key: %w", err)
		}
	}

	var expiryVal interface{}
	if creds.Token != nil && !creds.Token.Expiry.IsZero() {
		expiryVal = creds.Token.Expiry
	}

	query := `INSERT INTO credentials (instance_id, auth_type, encrypted_access_token, encrypted_refresh_token, token_expiry, encrypted_api_key)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			auth_type = excluded.auth_type,
			encrypted_access_token = excluded.encrypted_access_token,
			encrypted_refresh_token = excluded.encrypted_refresh_token,
			token_expiry = excluded.token_expiry,
			encrypted_api_key = excluded.encrypted_api_key;`

	_, err = db.db.Exec(query, instanceID, string(authType), encToken, encRefresh, expiryVal, encAPIKey)
	return err
}

func (db *DB) GetCredentials(instanceID string) (*sdk.Credentials, sdk.AuthType, error) {
	query := `SELECT auth_type, encrypted_access_token, encrypted_refresh_token, token_expiry, encrypted_api_key FROM credentials WHERE instance_id = ?;`
	row := db.db.QueryRow(query, instanceID)

	var authTypeStr string
	var encToken, encRefresh, encAPIKey sql.NullString
	var tokenExpiry sql.NullTime

	err := row.Scan(&authTypeStr, &encToken, &encRefresh, &tokenExpiry, &encAPIKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("credentials not found for instance: %s", instanceID)
		}
		return nil, "", err
	}

	authType := sdk.AuthType(authTypeStr)
	var creds sdk.Credentials

	if authType == sdk.AuthTypeOAuth2 && encToken.Valid {
		decToken, err := db.decrypt(encToken.String)
		if err != nil {
			return nil, "", fmt.Errorf("failed to decrypt access token: %w", err)
		}
		decRefresh, err := db.decrypt(encRefresh.String)
		if err != nil {
			return nil, "", fmt.Errorf("failed to decrypt refresh token: %w", err)
		}

		var expiry time.Time
		if tokenExpiry.Valid {
			expiry = tokenExpiry.Time
		}

		creds.Token = &sdk.Token{
			AccessToken:  decToken,
			RefreshToken: decRefresh,
			Expiry:       expiry,
		}
	} else if authType == sdk.AuthTypeAPIKey && encAPIKey.Valid {
		decAPIKey, err := db.decrypt(encAPIKey.String)
		if err != nil {
			return nil, "", fmt.Errorf("failed to decrypt api key: %w", err)
		}
		creds.APIKey = decAPIKey
	}

	return &creds, authType, nil
}
