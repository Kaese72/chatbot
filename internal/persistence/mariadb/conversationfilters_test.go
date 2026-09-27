package mariadb

import (
	"testing"

	"github.com/Kaese72/huemie-lib/query"
)

// These exercise conversationFilters/conversationSortFields directly (no DB
// needed, since query.Translate/BuildOrderBy are pure functions) - they catch
// a typo'd column name or operator wiring without needing testcontainers/
// Docker. Critically, they also assert owner_id can never be filtered on -
// that's the privacy invariant ListConversations depends on.
func TestConversationFilters(t *testing.T) {
	t.Run("status eq", func(t *testing.T) {
		fragments, args, err := query.Translate([]query.Filter{
			{Field: "status", Operator: "eq", Value: "USER_INPUT"},
		}, conversationFilters)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fragments) != 1 || len(args) != 1 {
			t.Fatalf("unexpected result: fragments=%v args=%v", fragments, args)
		}
	})

	t.Run("name text-contains", func(t *testing.T) {
		fragments, args, err := query.Translate([]query.Filter{
			{Field: "name", Operator: "text-contains", Value: "hue"},
		}, conversationFilters)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fragments) != 1 || len(args) != 1 {
			t.Fatalf("unexpected result: fragments=%v args=%v", fragments, args)
		}
	})

	t.Run("owner_id can never be filtered on", func(t *testing.T) {
		if _, _, err := query.Translate([]query.Filter{
			{Field: "owner_id", Operator: "eq", Value: "1"},
		}, conversationFilters); err == nil {
			t.Fatal("expected an error - owner_id must not be a filterable field")
		}
	})
}

func TestConversationSortFields(t *testing.T) {
	t.Run("empty falls back to updated DESC", func(t *testing.T) {
		clause, err := query.BuildOrderBy(nil, conversationSortFields, "updated DESC")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if clause != "updated DESC" {
			t.Fatalf("expected fallback \"updated DESC\", got %q", clause)
		}
	})

	t.Run("sort by created asc", func(t *testing.T) {
		clause, err := query.BuildOrderBy([]query.Sort{{Field: "created", Direction: "asc"}}, conversationSortFields, "updated DESC")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if clause != "created ASC" {
			t.Fatalf("unexpected clause: %q", clause)
		}
	})

	t.Run("owner_id can never be sorted on either", func(t *testing.T) {
		if _, err := query.BuildOrderBy([]query.Sort{{Field: "owner_id", Direction: "asc"}}, conversationSortFields, "updated DESC"); err == nil {
			t.Fatal("expected an error - owner_id must not be a sortable field")
		}
	})
}
