package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrParameterSetNotFound is returned when a set name does not exist (ADR D20).
//
// It is an ERROR and not an empty set on purpose: an unknown set must abort the
// score job instead of silently producing a score with the defaults, because
// "I asked for conservative and got base" is indistinguishable from "I asked for
// conservative and got conservative" if you only look at the number.
var ErrParameterSetNotFound = errors.New("storage: parameter set no encontrado")

const parameterSetColumns = `id, name, model_version, parameters, created_at`

// UpsertParameterSet creates or REPLACES a parameter set. Unlike the seeds of
// migration 015 (ON CONFLICT DO NOTHING, which protects an operator's edit), an
// explicit upsert from code is an explicit intent to overwrite.
func UpsertParameterSet(ctx context.Context, tx pgx.Tx, p *ParameterSet) (int64, error) {
	if p.Name == "" {
		return 0, fmt.Errorf("storage: parameter set sin nombre")
	}
	if p.ModelVersion == "" {
		return 0, fmt.Errorf("storage: parameter set %q sin model_version", p.Name)
	}
	params := p.Parameters
	if len(params) == 0 {
		params = []byte(`{}`)
	}
	var id int64
	err := tx.QueryRow(ctx, `
INSERT INTO parameter_sets (name, model_version, parameters)
VALUES ($1, $2, $3)
ON CONFLICT (name) DO UPDATE SET
    model_version = EXCLUDED.model_version,
    parameters    = EXCLUDED.parameters
RETURNING id`, p.Name, p.ModelVersion, params).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("storage: upsert parameter set %q: %w", p.Name, err)
	}
	p.ID = id
	return id, nil
}

// GetParameterSetByName returns one set by name, or ErrParameterSetNotFound.
func GetParameterSetByName(ctx context.Context, q DBTX, name string) (*ParameterSet, error) {
	var p ParameterSet
	var raw []byte
	err := q.QueryRow(ctx, `SELECT `+parameterSetColumns+` FROM parameter_sets WHERE name = $1`, name).
		Scan(&p.ID, &p.Name, &p.ModelVersion, &raw, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrParameterSetNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: get parameter set %q: %w", name, err)
	}
	p.Parameters = raw
	return &p, nil
}

// ListParameterSets returns every set ordered by name (deterministic: /parameter-sets
// and the CLI both render this list).
func ListParameterSets(ctx context.Context, q DBTX) ([]ParameterSet, error) {
	rows, err := q.Query(ctx, `SELECT `+parameterSetColumns+` FROM parameter_sets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("storage: list parameter sets: %w", err)
	}
	defer rows.Close()
	out := []ParameterSet{}
	for rows.Next() {
		var p ParameterSet
		var raw []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.ModelVersion, &raw, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan parameter set: %w", err)
		}
		p.Parameters = raw
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: rows parameter sets: %w", err)
	}
	return out, nil
}
