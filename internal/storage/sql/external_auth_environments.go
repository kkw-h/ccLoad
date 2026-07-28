package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"ccLoad/internal/model"
)

func (s *SQLStore) CreateExternalAuthEnvironment(ctx context.Context, env *model.ExternalAuthEnvironment) (*model.ExternalAuthEnvironment, error) {
	now := timeToUnix(time.Now())
	id := env.ID
	var err error
	if id != 0 {
		_, err = s.ExecContext(ctx, `
			INSERT INTO external_auth_environments(id, environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?)
		`, id, env.Environment, env.AuthzURL, boolToInt(env.IsActive), now, now)
	} else if s.IsPostgres() {
		err = s.QueryRowContext(ctx, `
			INSERT INTO external_auth_environments(environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?) RETURNING id
		`, env.Environment, env.AuthzURL, boolToInt(env.IsActive), now, now).Scan(&id)
	} else {
		var result sql.Result
		result, err = s.ExecContext(ctx, `
			INSERT INTO external_auth_environments(environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?)
		`, env.Environment, env.AuthzURL, boolToInt(env.IsActive), now, now)
		if err == nil {
			id, err = result.LastInsertId()
		}
	}
	if err != nil {
		if isExternalAuthEnvironmentConflict(err) {
			return nil, model.ErrExternalAuthEnvironmentConflict
		}
		return nil, fmt.Errorf("create external auth environment: %w", err)
	}
	return &model.ExternalAuthEnvironment{
		ID: id, Environment: env.Environment, AuthzURL: env.AuthzURL,
		IsActive: env.IsActive, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *SQLStore) ListExternalAuthEnvironments(ctx context.Context) ([]*model.ExternalAuthEnvironment, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT id, environment, authz_url, is_active, created_at, updated_at
		FROM external_auth_environments ORDER BY environment ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list external auth environments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]*model.ExternalAuthEnvironment, 0)
	for rows.Next() {
		var env model.ExternalAuthEnvironment
		var active int
		if err := rows.Scan(&env.ID, &env.Environment, &env.AuthzURL, &active, &env.CreatedAt, &env.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan external auth environment: %w", err)
		}
		env.IsActive = active != 0
		result = append(result, &env)
	}
	return result, rows.Err()
}

func (s *SQLStore) UpdateExternalAuthEnvironment(ctx context.Context, env *model.ExternalAuthEnvironment) (*model.ExternalAuthEnvironment, error) {
	now := timeToUnix(time.Now())
	result, err := s.ExecContext(ctx, `
		UPDATE external_auth_environments
		SET environment = ?, authz_url = ?, is_active = ?, updated_at = ?
		WHERE id = ?
	`, env.Environment, env.AuthzURL, boolToInt(env.IsActive), now, env.ID)
	if err != nil {
		if isExternalAuthEnvironmentConflict(err) {
			return nil, model.ErrExternalAuthEnvironmentConflict
		}
		return nil, fmt.Errorf("update external auth environment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("check external auth environment update: %w", err)
	}
	if rows == 0 {
		return nil, model.ErrExternalAuthEnvironmentNotFound
	}
	updated := *env
	updated.UpdatedAt = now
	return &updated, nil
}

func (s *SQLStore) DeleteExternalAuthEnvironment(ctx context.Context, id int64) error {
	result, err := s.ExecContext(ctx, `DELETE FROM external_auth_environments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete external auth environment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check external auth environment delete: %w", err)
	}
	if rows == 0 {
		return model.ErrExternalAuthEnvironmentNotFound
	}
	return nil
}

func isExternalAuthEnvironmentConflict(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr interface{ Number() uint16 }
	if errors.As(err, &mysqlErr) && mysqlErr.Number() == mysqlDuplicateEntryCode {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "sqlstate 23505")
}
