package discord

import (
	"strings"
	"testing"
)

func TestBuildTriggerSQL_NoWhenClause(t *testing.T) {
	cols := []string{"id", "email", "name"}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "New member: {email}", cols, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER INSERT ON members") {
		t.Errorf("expected AFTER INSERT ON members, got:\n%s", sql)
	}
	if !strings.Contains(sql, "REPLACE(w.message_template, '{email}', COALESCE(CAST(NEW.email AS TEXT), ''))") {
		t.Errorf("expected template substitution for email, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_EmptyWhenClause(t *testing.T) {
	cols := []string{"id", "email"}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, "   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for whitespace-only when_clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_SimpleWhenClause(t *testing.T) {
	cols := []string{"id", "event_type", "member_id"}
	sql, err := buildTriggerSQL(42, "member_events", "INSERT", "Event: {event_type}", cols, "NEW.event_type = 'EmailConfirmed'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.event_type = 'EmailConfirmed'") {
		t.Errorf("expected WHEN clause, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER INSERT ON member_events") {
		t.Errorf("expected AFTER INSERT ON member_events, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_MultipleConditionsInWhenClause(t *testing.T) {
	cols := []string{"id", "event_type", "member_id", "status"}
	sql, err := buildTriggerSQL(5, "member_events", "INSERT", "{event_type}", cols, "NEW.event_type = 'AccessStatusChanged' AND NEW.status != 'deleted'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.event_type = 'AccessStatusChanged' AND NEW.status != 'deleted'") {
		t.Errorf("expected compound WHEN clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_DeleteUsesOLD(t *testing.T) {
	cols := []string{"id", "email", "status"}
	sql, err := buildTriggerSQL(3, "members", "DELETE", "Deleted: {email}", cols, "OLD.status = 'active'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN OLD.status = 'active'") {
		t.Errorf("expected OLD reference in WHEN clause for DELETE trigger, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER DELETE ON members") {
		t.Errorf("expected AFTER DELETE, got:\n%s", sql)
	}
	if !strings.Contains(sql, "COALESCE(CAST(OLD.email AS TEXT), '')") {
		t.Errorf("expected OLD reference for template substitution, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidOperation(t *testing.T) {
	cols := []string{"id"}
	_, err := buildTriggerSQL(1, "members", "TRUNCATE", "test", cols, "")
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestBuildTriggerSQL_WhenClauseWithORLogic(t *testing.T) {
	cols := []string{"id", "event_type", "status", "amount"}
	sql, err := buildTriggerSQL(10, "payments", "INSERT", "Payment: {amount}", cols, "NEW.event_type = 'PaymentReceived' OR NEW.status = 'active'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "WHEN NEW.event_type = 'PaymentReceived' OR NEW.status = 'active'"
	if !strings.Contains(sql, expected) {
		t.Errorf("expected OR condition in WHEN clause:\n%s\ngot:\n%s", expected, sql)
	}
}

func TestBuildTriggerSQL_WhenClausePreservedVerbatim(t *testing.T) {
	cols := []string{"id", "email"}
	whenClause := "NEW.email LIKE '%@example.com' AND NEW.id > 100"
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, whenClause)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN "+whenClause) {
		t.Errorf("expected WHEN clause to be preserved verbatim, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_UpdateOperation(t *testing.T) {
	cols := []string{"id", "email", "name"}
	sql, err := buildTriggerSQL(7, "members", "UPDATE", "Updated: {email}", cols, "NEW.email IS NOT NULL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AFTER UPDATE ON members") {
		t.Errorf("expected AFTER UPDATE, got:\n%s", sql)
	}
	if !strings.Contains(sql, "WHEN NEW.email IS NOT NULL") {
		t.Errorf("expected WHEN clause, got:\n%s", sql)
	}
}
