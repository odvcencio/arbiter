package factsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLoadPostgres(t *testing.T) {
	origOpen := openPostgresDB
	t.Cleanup(func() { openPostgresDB = origOpen })

	var gotDSN, gotQuery string
	openPostgresDB = func(dsn string) (postgresDB, error) {
		gotDSN = dsn
		return fakePostgresDB{
			queryFunc: func(_ context.Context, query string, _ ...any) (postgresRows, error) {
				gotQuery = query
				return &fakePostgresRows{rows: [][]any{
					{"Lead", "acct-1", []byte(`{"score":95}`), int64(7)},
				}}, nil
			},
		}, nil
	}

	facts, err := Load("postgres://arbiter:secret@db.internal/sales?sslmode=disable&table=facts&schema=governance&mode=merge")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotDSN != "postgres://arbiter:secret@db.internal/sales?sslmode=disable" {
		t.Fatalf("dsn = %q", gotDSN)
	}
	if !strings.Contains(gotQuery, `FROM "governance"."facts"`) {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(facts) != 1 || facts[0].Version != 7 {
		t.Fatalf("facts = %+v", facts)
	}
	if facts[0].Fields["score"] != float64(95) || facts[0].Fields["key"] != "acct-1" {
		t.Fatalf("fields = %+v", facts[0].Fields)
	}
}

func TestSavePostgresReplaceMode(t *testing.T) {
	origOpen := openPostgresDB
	t.Cleanup(func() { openPostgresDB = origOpen })

	var (
		gotDSN      string
		beginOpts   *sql.TxOptions
		queryString string
		execCalls   []fakeExecCall
		committed   bool
	)
	openPostgresDB = func(dsn string) (postgresDB, error) {
		gotDSN = dsn
		return fakePostgresDB{
			beginFunc: func(_ context.Context, opts *sql.TxOptions) (postgresTx, error) {
				beginOpts = opts
				return fakePostgresTx{
					queryFunc: func(_ context.Context, query string, _ ...any) (postgresRows, error) {
						queryString = query
						return &fakePostgresRows{rows: [][]any{
							{"Lead", "acct-1", []byte(`{"score":90}`), int64(4)},
							{"Lead", "stale", []byte(`{"score":10}`), int64(2)},
						}}, nil
					},
					execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
						execCalls = append(execCalls, fakeExecCall{query: query, args: args})
						return fakeResult(1), nil
					},
					commitFunc: func() error {
						committed = true
						return nil
					},
				}, nil
			},
		}, nil
	}

	err := Save("postgres://arbiter:secret@db.internal/sales?sslmode=disable&table=facts&schema=governance", []Fact{
		{
			Type:    "Lead",
			Key:     "acct-1",
			Version: 4,
			Fields: map[string]any{
				"score": 95.0,
				"key":   "acct-1",
			},
		},
		{
			Type: "Lead",
			Key:  "acct-2",
			Fields: map[string]any{
				"score": 88.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if gotDSN != "postgres://arbiter:secret@db.internal/sales?sslmode=disable" {
		t.Fatalf("dsn = %q", gotDSN)
	}
	if beginOpts == nil || beginOpts.Isolation != sql.LevelSerializable {
		t.Fatalf("begin opts = %+v", beginOpts)
	}
	if !strings.Contains(queryString, "FOR UPDATE") {
		t.Fatalf("query = %q", queryString)
	}
	if !committed {
		t.Fatal("expected commit")
	}
	if len(execCalls) != 3 {
		t.Fatalf("exec calls = %+v", execCalls)
	}
	if !strings.HasPrefix(execCalls[0].query, `UPDATE "governance"."facts"`) {
		t.Fatalf("update query = %q", execCalls[0].query)
	}
	if payload, ok := execCalls[0].args[2].([]byte); !ok || string(payload) != `{"score":95}` {
		t.Fatalf("update payload = %#v", execCalls[0].args[2])
	}
	if execCalls[0].args[3] != int64(5) {
		t.Fatalf("update version = %#v", execCalls[0].args[3])
	}
	if !strings.HasPrefix(execCalls[1].query, `INSERT INTO "governance"."facts"`) {
		t.Fatalf("insert query = %q", execCalls[1].query)
	}
	if payload, ok := execCalls[1].args[2].([]byte); !ok || string(payload) != `{"score":88}` {
		t.Fatalf("insert payload = %#v", execCalls[1].args[2])
	}
	if !strings.HasPrefix(execCalls[2].query, `DELETE FROM "governance"."facts"`) {
		t.Fatalf("delete query = %q", execCalls[2].query)
	}
	if execCalls[2].args[0] != "Lead" || execCalls[2].args[1] != "stale" {
		t.Fatalf("delete args = %+v", execCalls[2].args)
	}
}

func TestSavePostgresMergeModeSkipsDeletes(t *testing.T) {
	origOpen := openPostgresDB
	t.Cleanup(func() { openPostgresDB = origOpen })

	var execCalls []fakeExecCall
	openPostgresDB = func(string) (postgresDB, error) {
		return fakePostgresDB{
			beginFunc: func(_ context.Context, _ *sql.TxOptions) (postgresTx, error) {
				return fakePostgresTx{
					queryFunc: func(_ context.Context, _ string, _ ...any) (postgresRows, error) {
						return &fakePostgresRows{rows: [][]any{
							{"Lead", "stale", []byte(`{"score":10}`), int64(2)},
						}}, nil
					},
					execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
						execCalls = append(execCalls, fakeExecCall{query: query, args: args})
						return fakeResult(1), nil
					},
					commitFunc: func() error { return nil },
				}, nil
			},
		}, nil
	}

	err := Save("postgres://db.internal/sales?table=facts&mode=merge", []Fact{{
		Type: "Lead",
		Key:  "acct-2",
	}})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(execCalls) != 1 || !strings.HasPrefix(execCalls[0].query, `INSERT INTO "public"."facts"`) {
		t.Fatalf("exec calls = %+v", execCalls)
	}
}

func TestSavePostgresRejectsVersionConflicts(t *testing.T) {
	origOpen := openPostgresDB
	t.Cleanup(func() { openPostgresDB = origOpen })

	openPostgresDB = func(string) (postgresDB, error) {
		return fakePostgresDB{
			beginFunc: func(_ context.Context, _ *sql.TxOptions) (postgresTx, error) {
				return fakePostgresTx{
					queryFunc: func(_ context.Context, _ string, _ ...any) (postgresRows, error) {
						return &fakePostgresRows{rows: [][]any{
							{"Lead", "acct-1", []byte(`{"score":90}`), int64(4)},
						}}, nil
					},
				}, nil
			},
		}, nil
	}

	err := Save("postgres://db.internal/sales?table=facts", []Fact{{
		Type:    "Lead",
		Key:     "acct-1",
		Version: 3,
	}})
	if err == nil || !strings.Contains(err.Error(), "expected version 3, found 4") {
		t.Fatalf("expected version conflict, got %v", err)
	}
}

func TestParsePostgresTargetRejectsUnsafeIdentifiers(t *testing.T) {
	_, err := parsePostgresTarget("postgres://db.internal/sales?table=facts%3Bdrop")
	if err == nil || !strings.Contains(err.Error(), `invalid table "facts;drop"`) {
		t.Fatalf("expected unsafe identifier error, got %v", err)
	}
}

type fakePostgresDB struct {
	queryFunc func(context.Context, string, ...any) (postgresRows, error)
	beginFunc func(context.Context, *sql.TxOptions) (postgresTx, error)
}

func (db fakePostgresDB) QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error) {
	if db.queryFunc == nil {
		return nil, errors.New("unexpected db query")
	}
	return db.queryFunc(ctx, query, args...)
}

func (db fakePostgresDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (postgresTx, error) {
	if db.beginFunc == nil {
		return nil, errors.New("unexpected begin")
	}
	return db.beginFunc(ctx, opts)
}

func (db fakePostgresDB) Close() error { return nil }

type fakePostgresTx struct {
	queryFunc    func(context.Context, string, ...any) (postgresRows, error)
	execFunc     func(context.Context, string, ...any) (sql.Result, error)
	commitFunc   func() error
	rollbackFunc func() error
}

func (tx fakePostgresTx) QueryContext(ctx context.Context, query string, args ...any) (postgresRows, error) {
	if tx.queryFunc == nil {
		return nil, errors.New("unexpected tx query")
	}
	return tx.queryFunc(ctx, query, args...)
}

func (tx fakePostgresTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx.execFunc == nil {
		return nil, errors.New("unexpected tx exec")
	}
	return tx.execFunc(ctx, query, args...)
}

func (tx fakePostgresTx) Commit() error {
	if tx.commitFunc == nil {
		return nil
	}
	return tx.commitFunc()
}

func (tx fakePostgresTx) Rollback() error {
	if tx.rollbackFunc == nil {
		return nil
	}
	return tx.rollbackFunc()
}

type fakePostgresRows struct {
	rows   [][]any
	index  int
	closed bool
}

func (r *fakePostgresRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *fakePostgresRows) Scan(dest ...any) error {
	row := r.rows[r.index-1]
	for i := range dest {
		switch target := dest[i].(type) {
		case *string:
			*target = row[i].(string)
		case *[]byte:
			switch value := row[i].(type) {
			case []byte:
				*target = append((*target)[:0], value...)
			case string:
				*target = []byte(value)
			default:
				data, err := json.Marshal(value)
				if err != nil {
					return err
				}
				*target = data
			}
		case *int64:
			*target = row[i].(int64)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func (r *fakePostgresRows) Err() error   { return nil }
func (r *fakePostgresRows) Close() error { r.closed = true; return nil }

type fakeExecCall struct {
	query string
	args  []any
}

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return 0, errors.New("unsupported") }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }
