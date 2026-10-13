package cold

import (
	"context"
	"database/sql"
	"fmt"
)

// scanRecvBinds maps recv_binds rows in canonical column order.
func scanRecvBinds(rows *sql.Rows) ([]RecvBindRow, error) {
	var out []RecvBindRow
	for rows.Next() {
		var r RecvBindRow
		if err := rows.Scan(&r.FilePath, &r.Owner, &r.Recv, &r.Type, &r.ViaCall); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// insertRecvBindsTx writes bindings inside tx, skipping invalid rows.
func insertRecvBindsTx(ctx context.Context, tx *sql.Tx, v *Vault, rows []RecvBindRow) error {
	stmt := tx.StmtContext(ctx, v.stmts.insertRecvBind)
	defer stmt.Close()
	for _, r := range rows {
		if !r.Valid() {
			continue
		}
		if _, err := stmt.ExecContext(ctx, r.FilePath, r.Owner, r.Recv, r.Type, r.ViaCall); err != nil {
			return fmt.Errorf("cold: catalog recv bind %s %s: %w", r.FilePath, r.Recv, err)
		}
	}
	return nil
}

// CatalogRecvBinds appends receiver bindings in one transaction; clearing
// stays the replacer's job.
func (v *Vault) CatalogRecvBinds(ctx context.Context, rows []RecvBindRow) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		return insertRecvBindsTx(ctx, tx, v, rows)
	})
}

// FetchRecvBindsByFile returns one file's bindings, filtered by file_path.
func (v *Vault) FetchRecvBindsByFile(ctx context.Context, file string) ([]RecvBindRow, error) {
	rows, err := v.stmts.recvBindsByFile.QueryContext(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch recv binds %s: %w", file, err)
	}
	defer rows.Close()
	out, err := scanRecvBinds(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch recv binds %s: %w", file, err)
	}
	return out, nil
}

// FetchRecvBindsByType returns bindings claiming a type, filtered by
// type_name for the reverse-promotion pass.
func (v *Vault) FetchRecvBindsByType(ctx context.Context, typeName string) ([]RecvBindRow, error) {
	rows, err := v.stmts.recvBindsByType.QueryContext(ctx, typeName)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch recv binds by type %s: %w", typeName, err)
	}
	defer rows.Close()
	out, err := scanRecvBinds(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch recv binds by type %s: %w", typeName, err)
	}
	return out, nil
}
