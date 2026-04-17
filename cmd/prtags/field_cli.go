package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dutifuldev/prtags/internal/cli"
	"github.com/dutifuldev/prtags/internal/jsend"
	"github.com/spf13/cobra"
)

type fieldDefinitionView struct {
	ID              uint       `json:"id"`
	Name            string     `json:"name"`
	DisplayName     string     `json:"display_name"`
	ObjectScope     string     `json:"object_scope"`
	FieldType       string     `json:"field_type"`
	EnumValuesJSON  []string   `json:"enum_values_json"`
	IsRequired      bool       `json:"is_required"`
	IsFilterable    bool       `json:"is_filterable"`
	IsSearchable    bool       `json:"is_searchable"`
	IsVectorized    bool       `json:"is_vectorized"`
	SortOrder       int        `json:"sort_order"`
	RowVersion      int        `json:"row_version"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	RepositoryOwner string     `json:"repository_owner"`
	RepositoryName  string     `json:"repository_name"`
}

type desiredFieldDefinition struct {
	Name               string
	DisplayName        string
	ObjectScope        string
	FieldType          string
	EnumValues         []string
	IsRequired         bool
	IsFilterable       bool
	IsSearchable       bool
	IsVectorized       bool
	SortOrder          int
	ExplicitDisplay    bool
	ExplicitEnumValues bool
	ExplicitSortOrder  bool
}

type fieldListFilters struct {
	Name        string
	ObjectScope string
	FieldType   string
	ActiveOnly  bool
}

type fieldEnsureView struct {
	fieldDefinitionView
	Action string `json:"action"`
}

func fetchFieldDefinitions(ctx context.Context, serverURL, owner, repo string) ([]fieldDefinitionView, error) {
	client := cli.NewClient(serverURL)
	raw, err := client.DoJSON(ctx, "GET", fmt.Sprintf("/v1/repos/%s/%s/fields", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	data, err := cli.ExtractJSendData(raw)
	if err != nil {
		return nil, err
	}
	var fields []fieldDefinitionView
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func createFieldDefinition(ctx context.Context, serverURL, owner, repo string, payload map[string]any) (fieldDefinitionView, error) {
	client := cli.NewClient(serverURL)
	raw, err := client.DoJSON(ctx, "POST", fmt.Sprintf("/v1/repos/%s/%s/fields", owner, repo), payload)
	if err != nil {
		return fieldDefinitionView{}, err
	}
	return decodeFieldDefinitionEnvelope(raw)
}

func updateFieldDefinition(ctx context.Context, serverURL, owner, repo string, fieldID uint, payload map[string]any) (fieldDefinitionView, error) {
	client := cli.NewClient(serverURL)
	raw, err := client.DoJSON(ctx, "PATCH", fmt.Sprintf("/v1/repos/%s/%s/fields/%d", owner, repo, fieldID), payload)
	if err != nil {
		return fieldDefinitionView{}, err
	}
	return decodeFieldDefinitionEnvelope(raw)
}

func decodeFieldDefinitionEnvelope(raw []byte) (fieldDefinitionView, error) {
	data, err := cli.ExtractJSendData(raw)
	if err != nil {
		return fieldDefinitionView{}, err
	}
	var field fieldDefinitionView
	if err := json.Unmarshal(data, &field); err != nil {
		return fieldDefinitionView{}, err
	}
	return field, nil
}

func parseDesiredFieldDefinition(cmd flagReader) desiredFieldDefinition {
	return desiredFieldDefinition{
		Name:               normalizeFieldNameCLI(cmd.flagValue("name")),
		DisplayName:        strings.TrimSpace(cmd.flagValue("display-name")),
		ObjectScope:        strings.TrimSpace(cmd.flagValue("scope")),
		FieldType:          strings.TrimSpace(cmd.flagValue("type")),
		EnumValues:         normalizeEnumValuesCLI(parseCSVOrSpaceList(cmd.flagValue("enum-values"))),
		IsRequired:         cmd.boolFlagValue("required"),
		IsFilterable:       cmd.boolFlagValue("filterable"),
		IsSearchable:       cmd.boolFlagValue("searchable"),
		IsVectorized:       cmd.boolFlagValue("vectorized"),
		SortOrder:          cmd.intFlagValue("sort-order"),
		ExplicitDisplay:    cmd.flagChanged("display-name"),
		ExplicitEnumValues: cmd.flagChanged("enum-values"),
		ExplicitSortOrder:  cmd.flagChanged("sort-order"),
	}
}

func (d desiredFieldDefinition) createPayload() map[string]any {
	return map[string]any{
		"name":          d.Name,
		"display_name":  d.DisplayName,
		"object_scope":  d.ObjectScope,
		"field_type":    d.FieldType,
		"enum_values":   d.EnumValues,
		"is_required":   d.IsRequired,
		"is_filterable": d.IsFilterable,
		"is_searchable": d.IsSearchable,
		"is_vectorized": d.IsVectorized,
		"sort_order":    d.SortOrder,
	}
}

func diffFieldDefinition(existing fieldDefinitionView, desired desiredFieldDefinition) map[string]any {
	patch := map[string]any{}
	if desired.ExplicitDisplay && strings.TrimSpace(existing.DisplayName) != desired.DisplayName {
		patch["display_name"] = desired.DisplayName
	}
	if existing.IsRequired != desired.IsRequired {
		patch["is_required"] = desired.IsRequired
	}
	if existing.IsFilterable != desired.IsFilterable {
		patch["is_filterable"] = desired.IsFilterable
	}
	if existing.IsSearchable != desired.IsSearchable {
		patch["is_searchable"] = desired.IsSearchable
	}
	if existing.IsVectorized != desired.IsVectorized {
		patch["is_vectorized"] = desired.IsVectorized
	}
	if desired.ExplicitSortOrder && existing.SortOrder != desired.SortOrder {
		patch["sort_order"] = desired.SortOrder
	}
	if desired.ExplicitEnumValues && (existing.FieldType == "enum" || existing.FieldType == "multi_enum") {
		if !stringSlicesEqual(normalizeEnumValuesCLI(existing.EnumValuesJSON), desired.EnumValues) {
			patch["enum_values"] = desired.EnumValues
		}
	}
	return patch
}

func filterFieldDefinitions(fields []fieldDefinitionView, filters fieldListFilters) []fieldDefinitionView {
	out := make([]fieldDefinitionView, 0, len(fields))
	for _, field := range fields {
		if filters.ActiveOnly && field.ArchivedAt != nil {
			continue
		}
		if filters.Name != "" && field.Name != normalizeFieldNameCLI(filters.Name) {
			continue
		}
		if filters.ObjectScope != "" && field.ObjectScope != filters.ObjectScope {
			continue
		}
		if filters.FieldType != "" && field.FieldType != filters.FieldType {
			continue
		}
		out = append(out, field)
	}
	return out
}

func printFieldDefinitions(out io.Writer, fields []fieldDefinitionView, format string) error {
	switch format {
	case "json":
		return printJSendSuccess(out, fields)
	case "table":
		return renderFieldDefinitionTable(out, fields)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func renderFieldDefinitionTable(out io.Writer, fields []fieldDefinitionView) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tNAME\tSCOPE\tTYPE\tFLAGS\tDISPLAY\tSTATUS"); err != nil {
		return err
	}
	for _, field := range fields {
		status := "active"
		if field.ArchivedAt != nil {
			status = "archived"
		}
		display := field.DisplayName
		if strings.TrimSpace(display) == "" {
			display = "-"
		}
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			field.ID,
			field.Name,
			field.ObjectScope,
			field.FieldType,
			fieldCapabilityString(field),
			display,
			status,
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func fieldCapabilityString(field fieldDefinitionView) string {
	parts := make([]string, 0, 4)
	if field.IsRequired {
		parts = append(parts, "required")
	}
	if field.IsFilterable {
		parts = append(parts, "filterable")
	}
	if field.IsSearchable {
		parts = append(parts, "searchable")
	}
	if field.IsVectorized {
		parts = append(parts, "vectorized")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func printJSendSuccess(out io.Writer, data any) error {
	raw, err := json.Marshal(jsend.Success(data))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, prettyJSON(raw))
	return err
}

func normalizeFieldNameCLI(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func normalizeEnumValuesCLI(values []string) []string {
	set := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := set[value]; ok {
			continue
		}
		set[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return []string{}
	}
	sort.Strings(out)
	return out
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func defaultFieldListFormat(out io.Writer) string {
	file, ok := out.(*os.File)
	if !ok {
		return "json"
	}
	info, err := file.Stat()
	if err != nil {
		return "json"
	}
	if (info.Mode() & os.ModeCharDevice) != 0 {
		return "table"
	}
	return "json"
}

func parseCSVOrSpaceList(value string) []string {
	return strings.Fields(strings.ReplaceAll(value, ",", " "))
}

type flagReader interface {
	flagValue(name string) string
	boolFlagValue(name string) bool
	intFlagValue(name string) int
	flagChanged(name string) bool
}

type cobraFlagReader struct {
	cmd *cobra.Command
}

func (r cobraFlagReader) flagValue(name string) string {
	return r.cmd.Flag(name).Value.String()
}

func (r cobraFlagReader) boolFlagValue(name string) bool {
	return mustBoolFlag(r.cmd, name)
}

func (r cobraFlagReader) intFlagValue(name string) int {
	return mustIntFlag(r.cmd, name)
}

func (r cobraFlagReader) flagChanged(name string) bool {
	return r.cmd.Flags().Changed(name)
}
