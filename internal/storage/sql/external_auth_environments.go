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
	if env == nil {
		return nil, model.ErrInvalidExternalAuthEnvironment
	}
	environment, err := model.NormalizeExternalAuthEnvironment(env.Environment)
	if err != nil {
		return nil, err
	}
	now := timeToUnix(time.Now())
	id := env.ID
	active := externalAuthBoolInt(env.IsActive)
	if id != 0 {
		_, err = s.ExecContext(ctx, `
			INSERT INTO external_auth_environments(id, environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?)
		`, id, environment, env.AuthzURL, active, now, now)
	} else if s.IsPostgres() {
		err = s.QueryRowContext(ctx, `
			INSERT INTO external_auth_environments(environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?) RETURNING id
		`, environment, env.AuthzURL, active, now, now).Scan(&id)
	} else {
		var result sql.Result
		result, err = s.ExecContext(ctx, `
			INSERT INTO external_auth_environments(environment, authz_url, is_active, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?)
		`, environment, env.AuthzURL, active, now, now)
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
		ID: id, Environment: environment, AuthzURL: env.AuthzURL,
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
		env, scanErr := scanExternalAuthEnvironment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, env)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate external auth environments: %w", err)
	}
	return result, nil
}

func (s *SQLStore) GetExternalAuthEnvironment(ctx context.Context, id int64) (*model.ExternalAuthEnvironment, error) {
	env, err := scanExternalAuthEnvironment(s.QueryRowContext(ctx, `
		SELECT id, environment, authz_url, is_active, created_at, updated_at
		FROM external_auth_environments WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrExternalAuthEnvironmentNotFound
	}
	return env, err
}

func (s *SQLStore) UpdateExternalAuthEnvironment(ctx context.Context, id int64, env *model.ExternalAuthEnvironment) (*model.ExternalAuthEnvironment, error) {
	if env == nil {
		return nil, model.ErrInvalidExternalAuthEnvironment
	}
	environment, err := model.NormalizeExternalAuthEnvironment(env.Environment)
	if err != nil {
		return nil, err
	}
	now := timeToUnix(time.Now())
	result, err := s.ExecContext(ctx, `
		UPDATE external_auth_environments
		SET environment = ?, authz_url = ?, is_active = ?, updated_at = ?
		WHERE id = ?
	`, environment, env.AuthzURL, externalAuthBoolInt(env.IsActive), now, id)
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
	updated.ID = id
	updated.Environment = environment
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

type externalAuthEnvironmentScanner interface {
	Scan(dest ...any) error
}

func scanExternalAuthEnvironment(scanner externalAuthEnvironmentScanner) (*model.ExternalAuthEnvironment, error) {
	var env model.ExternalAuthEnvironment
	var active int16
	if err := scanner.Scan(&env.ID, &env.Environment, &env.AuthzURL, &active, &env.CreatedAt, &env.UpdatedAt); err != nil {
		return nil, err
	}
	env.IsActive = active != 0
	return &env, nil
}

func externalAuthBoolInt(value bool) int16 {
	if value {
		return 1
	}
	return 0
}

func isExternalAuthEnvironmentConflict(err error) bool {
	if err == nil {
		return false
	}
	if isMySQLDuplicateEntryError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "sqlstate 23505")
}
