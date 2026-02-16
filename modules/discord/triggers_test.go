package discord

import (
	"strings"
	"testing"
)

func TestBuildTriggerSQL_NoConditions(t *testing.T) {
	cols := []string{"id", "email", "name"}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "New member: {email}", cols, nil)
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

func TestBuildTriggerSQL_EmptyConditions(t *testing.T) {
	cols := []string{"id", "email"}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, []conditionRow{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for empty conditions, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_SingleCondition(t *testing.T) {
	cols := []string{"id", "event_type", "member_id"}
	conditions := []conditionRow{
		{ColumnName: "event_type", Operator: "=", Value: "EmailConfirmed", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(42, "member_events", "INSERT", "Event: {event_type}", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.event_type = 'EmailConfirmed'") {
		t.Errorf("expected WHEN clause with condition, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER INSERT ON member_events") {
		t.Errorf("expected AFTER INSERT ON member_events, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_MultipleANDConditions(t *testing.T) {
	cols := []string{"id", "event_type", "member_id", "status"}
	conditions := []conditionRow{
		{ColumnName: "event_type", Operator: "=", Value: "AccessStatusChanged", Logic: "AND"},
		{ColumnName: "status", Operator: "!=", Value: "deleted", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(5, "member_events", "INSERT", "{event_type}", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.event_type = 'AccessStatusChanged' AND NEW.status != 'deleted'") {
		t.Errorf("expected AND conditions, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_MixedANDORConditions(t *testing.T) {
	cols := []string{"id", "event_type", "status", "amount"}
	conditions := []conditionRow{
		{ColumnName: "event_type", Operator: "=", Value: "PaymentReceived", Logic: "AND"},
		{ColumnName: "status", Operator: "=", Value: "active", Logic: "OR"},
		{ColumnName: "amount", Operator: ">", Value: "100", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(10, "payments", "INSERT", "Payment: {amount}", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "WHEN NEW.event_type = 'PaymentReceived' OR NEW.status = 'active' AND NEW.amount > '100'"
	if !strings.Contains(sql, expected) {
		t.Errorf("expected mixed AND/OR conditions:\n%s\ngot:\n%s", expected, sql)
	}
}

func TestBuildTriggerSQL_UnaryOperators(t *testing.T) {
	cols := []string{"id", "email", "deleted_at"}
	conditions := []conditionRow{
		{ColumnName: "email", Operator: "IS NOT NULL", Value: "", Logic: "AND"},
		{ColumnName: "deleted_at", Operator: "IS NULL", Value: "", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(7, "members", "UPDATE", "Updated: {email}", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.email IS NOT NULL AND NEW.deleted_at IS NULL") {
		t.Errorf("expected unary operator conditions, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_DeleteUsesOLD(t *testing.T) {
	cols := []string{"id", "email", "status"}
	conditions := []conditionRow{
		{ColumnName: "status", Operator: "=", Value: "active", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(3, "members", "DELETE", "Deleted: {email}", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN OLD.status = 'active'") {
		t.Errorf("expected OLD reference for DELETE trigger, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER DELETE ON members") {
		t.Errorf("expected AFTER DELETE, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidOperatorSkipped(t *testing.T) {
	cols := []string{"id", "email"}
	conditions := []conditionRow{
		{ColumnName: "email", Operator: "DROP TABLE", Value: "x", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for invalid operator, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_UnknownColumnSkipped(t *testing.T) {
	cols := []string{"id", "email"}
	conditions := []conditionRow{
		{ColumnName: "nonexistent", Operator: "=", Value: "foo", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for unknown column, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_ValueWithQuotesEscaped(t *testing.T) {
	cols := []string{"id", "name"}
	conditions := []conditionRow{
		{ColumnName: "name", Operator: "=", Value: "O'Brien", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "NEW.name = 'O''Brien'") {
		t.Errorf("expected escaped single quote, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_LIKEOperator(t *testing.T) {
	cols := []string{"id", "email"}
	conditions := []conditionRow{
		{ColumnName: "email", Operator: "LIKE", Value: "%@example.com", Logic: "AND"},
	}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN NEW.email LIKE '%@example.com'") {
		t.Errorf("expected LIKE condition, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidLogicDefaultsToAND(t *testing.T) {
	cols := []string{"id", "email", "name"}
	conditions := []conditionRow{
		{ColumnName: "email", Operator: "=", Value: "a@b.com", Logic: "AND"},
		{ColumnName: "name", Operator: "=", Value: "test", Logic: "INVALID"},
	}
	sql, err := buildTriggerSQL(1, "members", "INSERT", "test", cols, conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AND NEW.name = 'test'") {
		t.Errorf("expected invalid logic to default to AND, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidOperation(t *testing.T) {
	cols := []string{"id"}
	_, err := buildTriggerSQL(1, "members", "TRUNCATE", "test", cols, nil)
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestBuildWhenClause_Empty(t *testing.T) {
	result := buildWhenClause(nil, map[string]bool{"id": true}, "NEW")
	if result != "" {
		t.Errorf("expected empty string, got: %q", result)
	}
}

func TestBuildWhenClause_AllInvalidConditions(t *testing.T) {
	conditions := []conditionRow{
		{ColumnName: "nonexistent", Operator: "=", Value: "foo", Logic: "AND"},
		{ColumnName: "id", Operator: "INVALID_OP", Value: "1", Logic: "AND"},
	}
	result := buildWhenClause(conditions, map[string]bool{"id": true}, "NEW")
	if result != "" {
		t.Errorf("expected empty string for all-invalid conditions, got: %q", result)
	}
}

func TestBuildWhenClause_MixedValidInvalid(t *testing.T) {
	conditions := []conditionRow{
		{ColumnName: "nonexistent", Operator: "=", Value: "foo", Logic: "AND"},
		{ColumnName: "id", Operator: "=", Value: "1", Logic: "AND"},
	}
	result := buildWhenClause(conditions, map[string]bool{"id": true}, "NEW")
	if result != "WHEN NEW.id = '1'\n" {
		t.Errorf("expected single valid condition, got: %q", result)
	}
}
