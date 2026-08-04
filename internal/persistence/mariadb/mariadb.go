// Package mariadb is the MariaDB-backed implementation of
// internal/persistence.Persistence.
package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Kaese72/chatbot/internal/config"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	_ "github.com/go-sql-driver/mysql"
)

type mariadbPersistence struct {
	db                 *sql.DB
	lockTimeoutSeconds int
}

// NewMariadbPersistence opens a MariaDB connection pool and returns a
// Persistence implementation backed by it. lockTimeoutSeconds is injected
// rather than read from the global config package so that the lock timeout
// used by every SQL statement here is explicit and testable.
func NewMariadbPersistence(conf config.DatabaseConfig, lockTimeoutSeconds int) (persistence.Persistence, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
		conf.User, conf.Password, conf.Host, conf.Port, conf.Database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	return &mariadbPersistence{db: db, lockTimeoutSeconds: lockTimeoutSeconds}, nil
}

func (p *mariadbPersistence) ListConversations(ctx context.Context) ([]restmodels.Conversation, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, status, initiative, name, lock_id, created, updated
		FROM conversations
		ORDER BY updated DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	conversations := []restmodels.Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (p *mariadbPersistence) GetConversation(ctx context.Context, conversationID int64) (restmodels.Conversation, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT id, status, initiative, name, lock_id, created, updated
		FROM conversations WHERE id = ?
	`, conversationID)
	c, err := scanConversation(row)
	if err == sql.ErrNoRows {
		return restmodels.Conversation{}, persistence.ErrConversationNotFound
	}
	return c, err
}

func (p *mariadbPersistence) BeginConversation(ctx context.Context, lockID string, query string) (restmodels.Conversation, error) {
	name := query
	if len(name) > 255 {
		name = name[:255]
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return restmodels.Conversation{}, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (status, initiative, name, lock_id, locked_at)
		VALUES ('AGENT_IN_PROGRESS', 'AGENT', ?, ?, NOW(6))
	`, name, lockID)
	if err != nil {
		return restmodels.Conversation{}, err
	}
	conversationID, err := res.LastInsertId()
	if err != nil {
		return restmodels.Conversation{}, err
	}

	if err := insertDialogEntry(ctx, tx, conversationID, persistence.NewDialogEntry{
		Type:       restmodels.DialogEntryTypeUserInput,
		Initiative: restmodels.InitiativeUser,
		Payload:    restmodels.UserInputPayload{Text: query},
	}); err != nil {
		return restmodels.Conversation{}, err
	}

	if err := tx.Commit(); err != nil {
		return restmodels.Conversation{}, err
	}
	return p.GetConversation(ctx, conversationID)
}

func (p *mariadbPersistence) BeginInput(ctx context.Context, conversationID int64, lockID string, query string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Step 1: the conversation must currently be USER-owned. This is the
	// "when the initiative is with the USER, the lock must not be
	// acquirable... when the user sends input, the initiative is changed
	// to AGENT first" guard from the README, and it also rejects
	// concurrent double-submission of input for the same conversation.
	initiativeResult, err := tx.ExecContext(ctx, `
		UPDATE conversations SET initiative = 'AGENT' WHERE id = ? AND initiative = 'USER'
	`, conversationID)
	if err != nil {
		return err
	}
	affected, err := initiativeResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		exists, err := conversationExists(ctx, tx, conversationID)
		if err != nil {
			return err
		}
		if !exists {
			return persistence.ErrConversationNotFound
		}
		return persistence.ErrConversationNotAwaitingInput
	}

	// Step 2: acquire the lock, only within the transaction that just
	// flipped initiative to AGENT, per the "then the lock is acquired
	// within that transaction" rule.
	lockResult, err := tx.ExecContext(ctx, `
		UPDATE conversations
		SET lock_id = ?, locked_at = NOW(6), status = 'AGENT_IN_PROGRESS'
		WHERE id = ? AND (lock_id IS NULL OR locked_at < NOW(6) - INTERVAL ? SECOND)
	`, lockID, conversationID, p.lockTimeoutSeconds)
	if err != nil {
		return err
	}
	affected, err = lockResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return persistence.ErrConversationLocked
	}

	if err := insertDialogEntry(ctx, tx, conversationID, persistence.NewDialogEntry{
		Type:       restmodels.DialogEntryTypeUserInput,
		Initiative: restmodels.InitiativeUser,
		Payload:    restmodels.UserInputPayload{Text: query},
	}); err != nil {
		return err
	}

	return tx.Commit()
}

func (p *mariadbPersistence) ReacquireLockAndAppend(ctx context.Context, conversationID int64, lockID string, entry persistence.NewDialogEntry) (restmodels.DialogEntry, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return restmodels.DialogEntry{}, err
	}
	defer tx.Rollback()

	// Deliberately does not re-check the lock-timeout window here: if
	// lock_id still equals our own lockID, nobody has stolen the lock yet
	// (stealing is the only way lock_id changes), so the expiry check
	// would be redundant.
	result, err := tx.ExecContext(ctx, `
		UPDATE conversations SET locked_at = NOW(6) WHERE id = ? AND lock_id = ?
	`, conversationID, lockID)
	if err != nil {
		return restmodels.DialogEntry{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return restmodels.DialogEntry{}, err
	}
	if affected == 0 {
		return restmodels.DialogEntry{}, persistence.ErrLockLost
	}

	id, contentJSON, err := insertDialogEntryReturningID(ctx, tx, conversationID, entry)
	if err != nil {
		return restmodels.DialogEntry{}, err
	}

	var created time.Time
	if err := tx.QueryRowContext(ctx, `SELECT created FROM dialog_entries WHERE id = ?`, id).Scan(&created); err != nil {
		return restmodels.DialogEntry{}, err
	}

	if err := tx.Commit(); err != nil {
		return restmodels.DialogEntry{}, err
	}

	de := restmodels.DialogEntry{
		ID:             id,
		Created:        created,
		ConversationID: conversationID,
		Type:           entry.Type,
		Initiative:     entry.Initiative,
	}
	if err := attachPayload(&de, contentJSON); err != nil {
		return restmodels.DialogEntry{}, err
	}
	return de, nil
}

func (p *mariadbPersistence) ReleaseLock(ctx context.Context, conversationID int64, lockID string) error {
	_, err := p.db.ExecContext(ctx, `
		UPDATE conversations
		SET initiative = 'USER', status = 'USER_INPUT', lock_id = NULL, locked_at = NULL
		WHERE id = ? AND lock_id = ?
	`, conversationID, lockID)
	return err
}

func (p *mariadbPersistence) ListDialogEntries(ctx context.Context, conversationID int64, afterID int64) ([]restmodels.DialogEntry, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, created, conversation_id, type, initiative, content
		FROM dialog_entries
		WHERE conversation_id = ? AND id > ?
		ORDER BY id ASC
	`, conversationID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []restmodels.DialogEntry{}
	for rows.Next() {
		var (
			de      restmodels.DialogEntry
			content []byte
		)
		if err := rows.Scan(&de.ID, &de.Created, &de.ConversationID, &de.Type, &de.Initiative, &content); err != nil {
			return nil, err
		}
		if err := attachPayload(&de, content); err != nil {
			return nil, err
		}
		entries = append(entries, de)
	}
	return entries, rows.Err()
}

func (p *mariadbPersistence) ForgetConversation(ctx context.Context, conversationID int64) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var initiative string
	err = tx.QueryRowContext(ctx, `SELECT initiative FROM conversations WHERE id = ? FOR UPDATE`, conversationID).Scan(&initiative)
	if err == sql.ErrNoRows {
		return persistence.ErrConversationNotFound
	}
	if err != nil {
		return err
	}
	if initiative != string(restmodels.InitiativeUser) {
		return persistence.ErrConversationNotAwaitingInput
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM dialog_entries WHERE conversation_id = ?`, conversationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, conversationID); err != nil {
		return err
	}

	return tx.Commit()
}

func (p *mariadbPersistence) ListAPIKeys(ctx context.Context) ([]restmodels.APIKey, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, name, type, active, created, updated
		FROM api_keys
		ORDER BY updated DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []restmodels.APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (p *mariadbPersistence) GetAPIKey(ctx context.Context, id int64) (restmodels.APIKey, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT id, name, type, active, created, updated
		FROM api_keys WHERE id = ?
	`, id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return restmodels.APIKey{}, persistence.ErrAPIKeyNotFound
	}
	return k, err
}

func (p *mariadbPersistence) CreateAPIKey(ctx context.Context, req restmodels.NewAPIKeyRequest) (restmodels.APIKey, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return restmodels.APIKey{}, err
	}
	defer tx.Rollback()

	if req.Active {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET active = FALSE WHERE active = TRUE`); err != nil {
			return restmodels.APIKey{}, err
		}
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO api_keys (name, type, active, value)
		VALUES (?, ?, ?, ?)
	`, req.Name, req.Type, req.Active, req.Value)
	if err != nil {
		return restmodels.APIKey{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return restmodels.APIKey{}, err
	}

	k, err := scanAPIKey(tx.QueryRowContext(ctx, `
		SELECT id, name, type, active, created, updated FROM api_keys WHERE id = ?
	`, id))
	if err != nil {
		return restmodels.APIKey{}, err
	}

	if err := tx.Commit(); err != nil {
		return restmodels.APIKey{}, err
	}
	return k, nil
}

func (p *mariadbPersistence) UpdateAPIKey(ctx context.Context, id int64, req restmodels.UpdateAPIKeyRequest) (restmodels.APIKey, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return restmodels.APIKey{}, err
	}
	defer tx.Rollback()

	exists, err := apiKeyExists(ctx, tx, id)
	if err != nil {
		return restmodels.APIKey{}, err
	}
	if !exists {
		return restmodels.APIKey{}, persistence.ErrAPIKeyNotFound
	}

	// Deactivate whichever other key is currently active *before* applying
	// this key's own fields, so a caller flipping this key active in the
	// same request as a rename doesn't race against itself.
	if req.Active != nil && *req.Active {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET active = FALSE WHERE active = TRUE AND id != ?`, id); err != nil {
			return restmodels.APIKey{}, err
		}
	}

	if req.Name != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET name = ? WHERE id = ?`, *req.Name, id); err != nil {
			return restmodels.APIKey{}, err
		}
	}
	if req.Value != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET value = ? WHERE id = ?`, *req.Value, id); err != nil {
			return restmodels.APIKey{}, err
		}
	}
	if req.Active != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET active = ? WHERE id = ?`, *req.Active, id); err != nil {
			return restmodels.APIKey{}, err
		}
	}

	k, err := scanAPIKey(tx.QueryRowContext(ctx, `
		SELECT id, name, type, active, created, updated FROM api_keys WHERE id = ?
	`, id))
	if err != nil {
		return restmodels.APIKey{}, err
	}

	if err := tx.Commit(); err != nil {
		return restmodels.APIKey{}, err
	}
	return k, nil
}

func (p *mariadbPersistence) DeleteAPIKey(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return persistence.ErrAPIKeyNotFound
	}
	return nil
}

func (p *mariadbPersistence) ActiveAPIKeyValue(ctx context.Context, keyType restmodels.APIKeyType) (string, error) {
	var value string
	err := p.db.QueryRowContext(ctx, `
		SELECT value FROM api_keys WHERE type = ? AND active = TRUE
	`, keyType).Scan(&value)
	if err == sql.ErrNoRows {
		return "", persistence.ErrNoActiveAPIKey
	}
	return value, err
}

func (p *mariadbPersistence) IdentityConfigured(ctx context.Context) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx, `SELECT 1 FROM identity WHERE id = 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (p *mariadbPersistence) SaveIdentity(ctx context.Context, username string, password string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM identity WHERE id = 1`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO identity (id, username, password) VALUES (1, ?, ?)
	`, username, password); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *mariadbPersistence) IdentityCredentials(ctx context.Context) (string, string, error) {
	var username, password string
	err := p.db.QueryRowContext(ctx, `
		SELECT username, password FROM identity WHERE id = 1
	`).Scan(&username, &password)
	if err == sql.ErrNoRows {
		return "", "", persistence.ErrIdentityNotConfigured
	}
	return username, password, err
}

// --- helpers ---

func apiKeyExists(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE id = ?`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// apiKeyRow is satisfied by both *sql.Row and *sql.Rows.
type apiKeyRow interface {
	Scan(dest ...any) error
}

func scanAPIKey(row apiKeyRow) (restmodels.APIKey, error) {
	var (
		k       restmodels.APIKey
		typeStr string
	)
	if err := row.Scan(&k.ID, &k.Name, &typeStr, &k.Active, &k.Created, &k.Updated); err != nil {
		return restmodels.APIKey{}, err
	}
	k.Type = restmodels.APIKeyType(typeStr)
	return k, nil
}

func conversationExists(ctx context.Context, tx *sql.Tx, conversationID int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, conversationID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func insertDialogEntry(ctx context.Context, tx *sql.Tx, conversationID int64, entry persistence.NewDialogEntry) error {
	_, _, err := insertDialogEntryReturningID(ctx, tx, conversationID, entry)
	return err
}

func insertDialogEntryReturningID(ctx context.Context, tx *sql.Tx, conversationID int64, entry persistence.NewDialogEntry) (int64, []byte, error) {
	contentJSON, err := json.Marshal(entry.Payload)
	if err != nil {
		return 0, nil, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO dialog_entries (conversation_id, type, initiative, content)
		VALUES (?, ?, ?, ?)
	`, conversationID, entry.Type, entry.Initiative, contentJSON)
	if err != nil {
		return 0, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}
	return id, contentJSON, nil
}

// conversationRow is satisfied by both *sql.Row and *sql.Rows.
type conversationRow interface {
	Scan(dest ...any) error
}

func scanConversation(row conversationRow) (restmodels.Conversation, error) {
	var (
		c      restmodels.Conversation
		status string
		init   string
		lockID sql.NullString
	)
	if err := row.Scan(&c.ID, &status, &init, &c.Name, &lockID, &c.Created, &c.Updated); err != nil {
		return restmodels.Conversation{}, err
	}
	c.Status = restmodels.ConversationStatus(status)
	c.Initiative = restmodels.Initiative(init)
	if lockID.Valid {
		c.Replica = &lockID.String
	}
	return c, nil
}

// attachPayload unmarshals raw (the dialog_entries.content column) into the
// DialogEntry field matching de.Type.
func attachPayload(de *restmodels.DialogEntry, raw []byte) error {
	switch de.Type {
	case restmodels.DialogEntryTypeUserInput:
		var p restmodels.UserInputPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.UserInput = &p
	case restmodels.DialogEntryTypeUserStop:
		// No payload.
	case restmodels.DialogEntryTypeAgentInitiativeReleased:
		// No payload.
	case restmodels.DialogEntryTypeAgentMessage:
		var p restmodels.AgentMessagePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.AgentMessage = &p
	case restmodels.DialogEntryTypeAgentError:
		var p restmodels.AgentErrorPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.AgentError = &p
	case restmodels.DialogEntryTypeAgentGenericToolCall:
		var p restmodels.GenericToolCallPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.GenericToolCall = &p
	case restmodels.DialogEntryTypeAgentGenericToolResponse:
		var p restmodels.GenericToolResponsePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.GenericToolResponse = &p
	case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerCall:
		var p restmodels.DeviceCapabilityTriggerCallPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.DeviceCapabilityTriggerCall = &p
	case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerResponse:
		var p restmodels.CapabilityTriggerResponsePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.DeviceCapabilityTriggerResponse = &p
	case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerCall:
		var p restmodels.GroupCapabilityTriggerCallPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.GroupCapabilityTriggerCall = &p
	case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerResponse:
		var p restmodels.CapabilityTriggerResponsePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		de.GroupCapabilityTriggerResponse = &p
	default:
		return fmt.Errorf("unknown dialog entry type %q", de.Type)
	}
	return nil
}
