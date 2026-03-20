package factsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	postgresSaveModeReplace postgresSaveMode = "replace"
	postgresSaveModeMerge   postgresSaveMode = "merge"
)

type postgresSaveMode string

type postgresTarget struct {
	DSN           string
	Schema        string
	Table         string
	TypeColumn    string
	KeyColumn     string
	FieldsColumn  string
	VersionColumn string
	Mode          postgresSaveMode
}

type postgresRowVersion struct {
	Type    string
	Key     string
	Version int64
}

type postgresDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (postgresTx, error)
	Close() error
}

type postgresTx interface {
	QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

type postgresRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type sqlPostgresDB struct {
	db *sql.DB
}

type sqlPostgresTx struct {
	tx *sql.Tx
}

type sqlPostgresRows struct {
	rows *sql.Rows
}

var openPostgresDB = defaultOpenPostgresDB

func init() {
	Register("postgres://", LoaderFunc(loadPostgres))
	Register("postgresql://", LoaderFunc(loadPostgres))
	RegisterSaver("postgres://", SaverFunc(savePostgres))
	RegisterSaver("postgresql://", SaverFunc(savePostgres))
}

func defaultOpenPostgresDB(dsn string) (postgresDB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	return sqlPostgresDB{db: db}, nil
}

func (db sqlPostgresDB) QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error) {
	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlPostgresRows{rows: rows}, nil
}

func (db sqlPostgresDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (postgresTx, error) {
	tx, err := db.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return sqlPostgresTx{tx: tx}, nil
}

func (db sqlPostgresDB) Close() error {
	return db.db.Close()
}

func (tx sqlPostgresTx) QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error) {
	rows, err := tx.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlPostgresRows{rows: rows}, nil
}

func (tx sqlPostgresTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(ctx, query, args...)
}

func (tx sqlPostgresTx) Commit() error {
	return tx.tx.Commit()
}

func (tx sqlPostgresTx) Rollback() error {
	return tx.tx.Rollback()
}

func (rows sqlPostgresRows) Next() bool {
	return rows.rows.Next()
}

func (rows sqlPostgresRows) Scan(dest ...any) error {
	return rows.rows.Scan(dest...)
}

func (rows sqlPostgresRows) Err() error {
	return rows.rows.Err()
}

func (rows sqlPostgresRows) Close() error {
	return rows.rows.Close()
}

func loadPostgres(uri string) ([]Fact, error) {
	target, err := parsePostgresTarget(uri)
	if err != nil {
		return nil, err
	}
	db, err := openPostgresDB(target.DSN)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(), target.selectFactsQuery(false))
	if err != nil {
		return nil, fmt.Errorf("postgres query: %w", err)
	}
	return readPostgresFacts(rows)
}

func savePostgres(uri string, facts []Fact) (err error) {
	target, err := parsePostgresTarget(uri)
	if err != nil {
		return err
	}
	db, err := openPostgresDB(target.DSN)
	if err != nil {
		return err
	}
	defer db.Close()

	incoming, err := normalizePostgresFacts(facts)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("postgres begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	current, err := loadCurrentPostgresRows(tx, target)
	if err != nil {
		return err
	}
	if err := applyPostgresFacts(tx, target, incoming, current); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres commit: %w", err)
	}
	return nil
}

func parsePostgresTarget(uri string) (postgresTarget, error) {
	parsed, err := parseMutableSourceURL(uri, "postgres", "postgresql")
	if err != nil {
		return postgresTarget{}, err
	}

	q := parsed.Query()
	target := postgresTarget{
		Schema:        firstNonEmptyString(q.Get("schema"), "public"),
		Table:         q.Get("table"),
		TypeColumn:    firstNonEmptyString(q.Get("type_column"), "type"),
		KeyColumn:     firstNonEmptyString(q.Get("key_column"), "key"),
		FieldsColumn:  firstNonEmptyString(q.Get("fields_column"), "fields"),
		VersionColumn: firstNonEmptyString(q.Get("version_column"), "version"),
		Mode:          postgresSaveMode(firstNonEmptyString(q.Get("mode"), string(postgresSaveModeReplace))),
	}
	if target.Table == "" {
		return postgresTarget{}, fmt.Errorf("postgres parse: table query parameter is required")
	}
	if target.Mode != postgresSaveModeReplace && target.Mode != postgresSaveModeMerge {
		return postgresTarget{}, fmt.Errorf("postgres parse: unsupported mode %q", target.Mode)
	}
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "schema", value: target.Schema},
		{label: "table", value: target.Table},
		{label: "type_column", value: target.TypeColumn},
		{label: "key_column", value: target.KeyColumn},
		{label: "fields_column", value: target.FieldsColumn},
		{label: "version_column", value: target.VersionColumn},
	} {
		if !isSafeSQLIdentifier(item.value) {
			return postgresTarget{}, fmt.Errorf("postgres parse: invalid %s %q", item.label, item.value)
		}
		q.Del(item.label)
	}
	q.Del("mode")
	parsed.RawQuery = q.Encode()
	target.DSN = parsed.String()
	return target, nil
}

func readPostgresFacts(rows postgresRows) ([]Fact, error) {
	defer rows.Close()
	out := make([]Fact, 0)
	for rows.Next() {
		var (
			typ     string
			key     string
			payload []byte
			version int64
		)
		if err := rows.Scan(&typ, &key, &payload, &version); err != nil {
			return nil, fmt.Errorf("postgres scan: %w", err)
		}
		fields, err := decodePostgresFields(payload)
		if err != nil {
			return nil, err
		}
		fields["key"] = key
		out = append(out, Fact{
			Type:    typ,
			Key:     key,
			Fields:  fields,
			Version: version,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres rows: %w", err)
	}
	return out, nil
}

func readPostgresVersions(rows postgresRows) ([]postgresRowVersion, error) {
	defer rows.Close()
	out := make([]postgresRowVersion, 0)
	for rows.Next() {
		var row postgresRowVersion
		var payload []byte
		if err := rows.Scan(&row.Type, &row.Key, &payload, &row.Version); err != nil {
			return nil, fmt.Errorf("postgres scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres rows: %w", err)
	}
	return out, nil
}

func normalizePostgresFacts(facts []Fact) ([]Fact, error) {
	sorted := sortFacts(facts)
	seen := make(map[string]struct{}, len(sorted))
	out := make([]Fact, 0, len(sorted))
	for _, fact := range sorted {
		if fact.Type == "" {
			return nil, fmt.Errorf("postgres save: fact type is required")
		}
		if fact.Key == "" {
			return nil, fmt.Errorf("postgres save: fact key is required")
		}
		id := fact.Type + "\x00" + fact.Key
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("postgres save: duplicate fact %s/%s", fact.Type, fact.Key)
		}
		seen[id] = struct{}{}
		out = append(out, Fact{
			Type:    fact.Type,
			Key:     fact.Key,
			Fields:  cloneFactFields(fact.Fields),
			Version: fact.Version,
		})
	}
	return out, nil
}

func marshalPostgresFields(fact Fact) ([]byte, error) {
	fields := cloneFactFields(fact.Fields)
	delete(fields, "type")
	delete(fields, "key")
	if fields == nil {
		fields = map[string]any{}
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("postgres encode %s/%s: %w", fact.Type, fact.Key, err)
	}
	return payload, nil
}

func decodePostgresFields(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return map[string]any{}, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("postgres decode: %w", err)
	}
	if fields == nil {
		fields = map[string]any{}
	}
	return fields, nil
}

func deleteMissingPostgresRows(tx postgresTx, target postgresTarget, current map[string]map[string]postgresRowVersion) error {
	for factType, byKey := range current {
		for factKey := range byKey {
			if _, err := tx.ExecContext(context.Background(), target.deleteFactQuery(), factType, factKey); err != nil {
				return fmt.Errorf("postgres delete %s/%s: %w", factType, factKey, err)
			}
		}
	}
	return nil
}

func loadCurrentPostgresRows(tx postgresTx, target postgresTarget) (map[string]map[string]postgresRowVersion, error) {
	rows, err := tx.QueryContext(context.Background(), target.selectFactsQuery(true))
	if err != nil {
		return nil, fmt.Errorf("postgres query: %w", err)
	}
	current, err := readPostgresVersions(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]postgresRowVersion, len(current))
	for _, row := range current {
		if out[row.Type] == nil {
			out[row.Type] = make(map[string]postgresRowVersion)
		}
		out[row.Type][row.Key] = row
	}
	return out, nil
}

func applyPostgresFacts(tx postgresTx, target postgresTarget, incoming []Fact, current map[string]map[string]postgresRowVersion) error {
	for _, fact := range incoming {
		if err := applyPostgresFact(tx, target, fact, current); err != nil {
			return err
		}
	}
	if target.Mode == postgresSaveModeReplace {
		if err := deleteMissingPostgresRows(tx, target, current); err != nil {
			return err
		}
	}
	return nil
}

func applyPostgresFact(tx postgresTx, target postgresTarget, fact Fact, current map[string]map[string]postgresRowVersion) error {
	byKey := current[fact.Type]
	currentRow, exists := byKey[fact.Key]
	payload, err := marshalPostgresFields(fact)
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(context.Background(), target.insertFactQuery(), fact.Type, fact.Key, payload, int64(1)); err != nil {
			return fmt.Errorf("postgres insert %s/%s: %w", fact.Type, fact.Key, err)
		}
		return nil
	}
	if fact.Version > 0 && fact.Version != currentRow.Version {
		return fmt.Errorf("postgres conflict %s/%s: expected version %d, found %d", fact.Type, fact.Key, fact.Version, currentRow.Version)
	}
	if _, err := tx.ExecContext(context.Background(), target.updateFactQuery(), fact.Type, fact.Key, payload, currentRow.Version+1); err != nil {
		return fmt.Errorf("postgres update %s/%s: %w", fact.Type, fact.Key, err)
	}
	delete(byKey, fact.Key)
	if len(byKey) == 0 {
		delete(current, fact.Type)
	}
	return nil
}

func (target postgresTarget) selectFactsQuery(lock bool) string {
	query := fmt.Sprintf(
		"SELECT %s, %s, COALESCE(%s, '{}'::jsonb), %s FROM %s ORDER BY %s, %s",
		target.quotedTypeColumn(),
		target.quotedKeyColumn(),
		target.quotedFieldsColumn(),
		target.quotedVersionColumn(),
		target.quotedTable(),
		target.quotedTypeColumn(),
		target.quotedKeyColumn(),
	)
	if lock {
		query += " FOR UPDATE"
	}
	return query
}

func (target postgresTarget) insertFactQuery() string {
	return fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s) VALUES ($1, $2, $3, $4)",
		target.quotedTable(),
		target.quotedTypeColumn(),
		target.quotedKeyColumn(),
		target.quotedFieldsColumn(),
		target.quotedVersionColumn(),
	)
}

func (target postgresTarget) updateFactQuery() string {
	return fmt.Sprintf(
		"UPDATE %s SET %s = $3, %s = $4 WHERE %s = $1 AND %s = $2",
		target.quotedTable(),
		target.quotedFieldsColumn(),
		target.quotedVersionColumn(),
		target.quotedTypeColumn(),
		target.quotedKeyColumn(),
	)
}

func (target postgresTarget) deleteFactQuery() string {
	return fmt.Sprintf(
		"DELETE FROM %s WHERE %s = $1 AND %s = $2",
		target.quotedTable(),
		target.quotedTypeColumn(),
		target.quotedKeyColumn(),
	)
}

func (target postgresTarget) quotedTable() string {
	return quoteSQLIdentifier(target.Schema) + "." + quoteSQLIdentifier(target.Table)
}

func (target postgresTarget) quotedTypeColumn() string {
	return quoteSQLIdentifier(target.TypeColumn)
}

func (target postgresTarget) quotedKeyColumn() string {
	return quoteSQLIdentifier(target.KeyColumn)
}

func (target postgresTarget) quotedFieldsColumn() string {
	return quoteSQLIdentifier(target.FieldsColumn)
}

func (target postgresTarget) quotedVersionColumn() string {
	return quoteSQLIdentifier(target.VersionColumn)
}

func quoteSQLIdentifier(s string) string {
	return `"` + s + `"`
}

func isSafeSQLIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
			continue
		case r >= 'a' && r <= 'z':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}
	return true
}

func parseMutableSourceURL(uri string, schemes ...string) (*url.URL, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", firstNonEmptyString(schemes...), err)
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			if parsed.Path == "" || parsed.Path == "/" {
				return nil, fmt.Errorf("%s parse: database name is required", scheme)
			}
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("unsupported source scheme %q", parsed.Scheme)
}

func cloneFactFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
