package cold

import (
	"context"
	"database/sql"
	"fmt"

	"srcinsight/internal/model"
)

// FetchTransportEndpointsByOwner returns server hints owned by one entry.
func (v *Vault) FetchTransportEndpointsByOwner(ctx context.Context, owner model.EntryID) ([]TransportEndpointRow, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, path, operation, framework, site_start FROM transport_endpoints WHERE owner = ? ORDER BY path, method`, string(owner))
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport endpoints %s: %w", owner, err)
	}
	defer rows.Close()
	return scanEndpointRows(rows)
}

// FetchTransportCallsByOwner returns client hints owned by one entry.
func (v *Vault) FetchTransportCallsByOwner(ctx context.Context, owner model.EntryID) ([]TransportCallRow, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, url, operation, lib, site_start FROM transport_calls WHERE owner = ? ORDER BY url, method`, string(owner))
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport calls %s: %w", owner, err)
	}
	defer rows.Close()
	return scanCallRows(rows)
}

// FetchTransportEndpointsByMethod returns server hints for a verb, capped.
// Empty method returns all HTTP endpoints; matching filters in Go.
func (v *Vault) FetchTransportEndpointsByMethod(ctx context.Context, method string, limit int) ([]TransportEndpointRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	var rows, err = v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, path, operation, framework, site_start FROM transport_endpoints WHERE (? = '' OR method = ?) ORDER BY path LIMIT ?`, method, method, limit)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport endpoints by method: %w", err)
	}
	defer rows.Close()
	return scanEndpointRows(rows)
}

// FetchTransportEndpointsByOperation returns GraphQL/WS endpoints by op name.
func (v *Vault) FetchTransportEndpointsByOperation(ctx context.Context, operation string, limit int) ([]TransportEndpointRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, path, operation, framework, site_start FROM transport_endpoints WHERE lower(operation) = lower(?) ORDER BY file_path LIMIT ?`, operation, limit)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport endpoints by operation: %w", err)
	}
	defer rows.Close()
	return scanEndpointRows(rows)
}

// FetchTransportCallsByOperation returns GraphQL/WS client hints by op name.
func (v *Vault) FetchTransportCallsByOperation(ctx context.Context, operation string, limit int) ([]TransportCallRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, url, operation, lib, site_start FROM transport_calls WHERE lower(operation) = lower(?) ORDER BY file_path LIMIT ?`, operation, limit)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport calls by operation: %w", err)
	}
	defer rows.Close()
	return scanCallRows(rows)
}

// FetchTransportCallsByMethod returns client hints for a verb, capped.
func (v *Vault) FetchTransportCallsByMethod(ctx context.Context, method string, limit int) ([]TransportCallRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := v.db.QueryContext(ctx,
		`SELECT file_path, owner, transport, method, url, operation, lib, site_start FROM transport_calls WHERE (? = '' OR method = ?) ORDER BY url LIMIT ?`, method, method, limit)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch transport calls by method: %w", err)
	}
	defer rows.Close()
	return scanCallRows(rows)
}

func scanEndpointRows(rows *sql.Rows) ([]TransportEndpointRow, error) {
	var out []TransportEndpointRow
	for rows.Next() {
		var r TransportEndpointRow
		var owner string
		if err := rows.Scan(&r.FilePath, &owner, &r.Transport, &r.Method, &r.Path, &r.Operation, &r.Framework, &r.SiteStart); err != nil {
			return nil, fmt.Errorf("cold: scan transport endpoint: %w", err)
		}
		r.Owner = model.EntryID(owner)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanCallRows(rows *sql.Rows) ([]TransportCallRow, error) {
	var out []TransportCallRow
	for rows.Next() {
		var r TransportCallRow
		var owner string
		if err := rows.Scan(&r.FilePath, &owner, &r.Transport, &r.Method, &r.URL, &r.Operation, &r.Lib, &r.SiteStart); err != nil {
			return nil, fmt.Errorf("cold: scan transport call: %w", err)
		}
		r.Owner = model.EntryID(owner)
		out = append(out, r)
	}
	return out, rows.Err()
}
