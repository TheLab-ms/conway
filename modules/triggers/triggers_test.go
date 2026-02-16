package triggers

import (
	"strings"
	"testing"
)

func TestBuildTriggerSQL_Basic(t *testing.T) {
	sql, err := buildTriggerSQL(1, "members", "INSERT", "", "INSERT INTO log (msg) VALUES ('new member');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AFTER INSERT ON members") {
		t.Errorf("expected AFTER INSERT ON members, got:\n%s", sql)
	}
	if !strings.Contains(sql, "user_trigger_1") {
		t.Errorf("expected trigger name user_trigger_1, got:\n%s", sql)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_WithWhenClause(t *testing.T) {
	sql, err := buildTriggerSQL(42, "members", "UPDATE", "OLD.confirmed = 0 AND NEW.confirmed = 1", "INSERT INTO member_events (member, event) VALUES (NEW.id, 'EmailConfirmed');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN OLD.confirmed = 0 AND NEW.confirmed = 1") {
		t.Errorf("expected WHEN clause, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER UPDATE ON members") {
		t.Errorf("expected AFTER UPDATE ON members, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_EmptyWhenClause(t *testing.T) {
	sql, err := buildTriggerSQL(1, "members", "INSERT", "   ", "INSERT INTO log (msg) VALUES ('test');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for whitespace-only when_clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_DeleteOperation(t *testing.T) {
	sql, err := buildTriggerSQL(3, "members", "DELETE", "OLD.status = 'active'", "INSERT INTO log (msg) VALUES ('deleted');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AFTER DELETE ON members") {
		t.Errorf("expected AFTER DELETE, got:\n%s", sql)
	}
	if !strings.Contains(sql, "WHEN OLD.status = 'active'") {
		t.Errorf("expected WHEN clause with OLD reference, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidOperation(t *testing.T) {
	_, err := buildTriggerSQL(1, "members", "TRUNCATE", "", "SELECT 1;")
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestBuildTriggerSQL_ActionSQLPreserved(t *testing.T) {
	action := "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'fob changed from ' || OLD.fob_id || ' to ' || NEW.fob_id);"
	sql, err := buildTriggerSQL(10, "members", "UPDATE", "OLD.fob_id != NEW.fob_id", action)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "INSERT INTO member_events") {
		t.Errorf("expected action SQL to be preserved, got:\n%s", sql)
	}
}

func TestTriggerName(t *testing.T) {
	name := triggerName(42)
	if name != "user_trigger_42" {
		t.Errorf("expected user_trigger_42, got %s", name)
	}
}
