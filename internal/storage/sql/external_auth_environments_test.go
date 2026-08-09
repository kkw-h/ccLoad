package sql_test

import (
	"context"
	"errors"
	"testing"

	"ccLoad/internal/model"
)

func TestExternalAuthEnvironmentCRUD(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "external_auth_environments.db")
	ctx := context.Background()

	created, err := store.CreateExternalAuthEnvironment(ctx, &model.ExternalAuthEnvironment{
		Environment: "develop",
		AuthzURL:    "https://sedna-dev.example.com/internal/llm/authz",
		IsActive:    true,
	})
	if err != nil {
		t.Fatalf("create external auth environment: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created ID = 0, want generated ID")
	}

	list, err := store.ListExternalAuthEnvironments(ctx)
	if err != nil {
		t.Fatalf("list external auth environments: %v", err)
	}
	if len(list) != 1 || list[0].Environment != "develop" || !list[0].IsActive {
		t.Fatalf("list = %#v, want active develop environment", list)
	}

	created.AuthzURL = "https://sedna-dev-2.example.com/internal/llm/authz"
	created.IsActive = false
	updated, err := store.UpdateExternalAuthEnvironment(ctx, created.ID, created)
	if err != nil {
		t.Fatalf("update external auth environment: %v", err)
	}
	if updated.AuthzURL != created.AuthzURL || updated.IsActive {
		t.Fatalf("updated = %#v, want changed URL and inactive", updated)
	}

	if err := store.DeleteExternalAuthEnvironment(ctx, created.ID); err != nil {
		t.Fatalf("delete external auth environment: %v", err)
	}
	if err := store.DeleteExternalAuthEnvironment(ctx, created.ID); !errors.Is(err, model.ErrExternalAuthEnvironmentNotFound) {
		t.Fatalf("second delete error = %v, want ErrExternalAuthEnvironmentNotFound", err)
	}
}

func TestExternalAuthEnvironmentUniqueName(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "external_auth_environment_unique.db")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := store.CreateExternalAuthEnvironment(ctx, &model.ExternalAuthEnvironment{
			Environment: "test",
			AuthzURL:    "https://sedna-test.example.com/internal/llm/authz",
			IsActive:    true,
		})
		if i == 0 && err != nil {
			t.Fatalf("first create: %v", err)
		}
		if i == 1 && !errors.Is(err, model.ErrExternalAuthEnvironmentConflict) {
			t.Fatalf("duplicate create error = %v, want ErrExternalAuthEnvironmentConflict", err)
		}
	}
}

func TestNormalizeExternalAuthEnvironment(t *testing.T) {
	t.Parallel()

	got, err := model.NormalizeExternalAuthEnvironment("  develop-1  ")
	if err != nil || got != "develop-1" {
		t.Fatalf("NormalizeExternalAuthEnvironment() = %q, %v", got, err)
	}
	for _, raw := range []string{"", "Develop", "dev space", "dev/one", "中文"} {
		if _, err := model.NormalizeExternalAuthEnvironment(raw); !errors.Is(err, model.ErrInvalidExternalAuthEnvironment) {
			t.Errorf("NormalizeExternalAuthEnvironment(%q) error = %v, want ErrInvalidExternalAuthEnvironment", raw, err)
		}
	}
}
