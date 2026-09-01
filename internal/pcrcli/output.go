package pcrcli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrclient"
)

func writeList(rt *Runtime, result *pcrclient.ListResult) error {
	if rt.Output == "jsonl" {
		return encodeJSONLines(rt.Stdout, result.Events)
	}
	if rt.Output == "table" {
		return writeEventTable(rt.Stdout, result.Events)
	}
	return encodeJSON(rt.Stdout, result)
}

func writeEvents(rt *Runtime, events []pcrclient.Event) error {
	if rt.Output == "jsonl" {
		return encodeJSONLines(rt.Stdout, events)
	}
	if rt.Output == "table" {
		return writeEventTable(rt.Stdout, events)
	}
	return encodeJSON(rt.Stdout, events)
}

func writeValue(rt *Runtime, value any) error {
	if rt.Output != "table" {
		return encodeJSON(rt.Stdout, value)
	}
	switch typed := value.(type) {
	case *pcrclient.Event:
		return writeEventTable(rt.Stdout, []pcrclient.Event{*typed})
	case doctorResult:
		return writeDoctorTable(rt.Stdout, typed)
	case *pcrclient.Annotations:
		_, err := fmt.Fprintf(rt.Stdout, "starred\t%t\nalerted\t%t\n", typed.Starred, typed.Alerted)
		return err
	case BuildInfo:
		_, err := fmt.Fprintf(rt.Stdout, "version\t%s\ncommit\t%s\nbuild date\t%s\n", tableCell(typed.Version), tableCell(typed.Commit), tableCell(typed.Date))
		return err
	case configPathResult:
		_, err := fmt.Fprintln(rt.Stdout, tableCell(typed.Path))
		return err
	case configShowResult:
		_, err := fmt.Fprintf(rt.Stdout, "path\t%s\nurl\t%s\nurl source\t%s\ncredential\t%s\ncredential source\t%s\n",
			tableCell(typed.Path), tableCell(typed.URL), tableCell(typed.URLSource), tableCell(typed.Credential), tableCell(typed.CredentialSource))
		return err
	case configWriteResult:
		_, err := fmt.Fprintf(rt.Stdout, "path\t%s\ncredential\t%s\n", tableCell(typed.Path), tableCell(typed.Credential))
		return err
	default:
		return encodeJSON(rt.Stdout, value)
	}
}

func encodeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

func encodeJSONLines(w io.Writer, events []pcrclient.Event) error {
	for i := range events {
		if err := encodeJSON(w, events[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeEventTable(w io.Writer, events []pcrclient.Event) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "ID\tTIME\tTYPE\tUSER\tDESCRIPTION"); err != nil {
		return fmt.Errorf("write table heading: %w", err)
	}
	for _, event := range events {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			tableCell(event.ID),
			tableCell(event.Timestamp.Format("2006-01-02T15:04:05Z07:00")),
			tableCell(event.EventType),
			tableCell(event.UserName),
			tableCell(event.Description),
		); err != nil {
			return fmt.Errorf("write event table: %w", err)
		}
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("flush event table: %w", err)
	}
	return nil
}

func writeDoctorTable(w io.Writer, result doctorResult) error {
	if _, err := fmt.Fprintf(w, "status: %s\n", tableCell(result.Status)); err != nil {
		return fmt.Errorf("write doctor status: %w", err)
	}
	for _, probe := range result.Probes {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", tableCell(probe.Name), tableCell(probe.Status), tableCell(probe.Detail)); err != nil {
			return fmt.Errorf("write doctor probe: %w", err)
		}
	}
	return nil
}

func tableCell(value string) string {
	var cleaned strings.Builder
	for _, r := range value {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			cleaned.WriteRune(' ')
		case isUnsafeTextRune(r):
			cleaned.WriteRune('?')
		default:
			cleaned.WriteRune(r)
		}
	}
	return cleaned.String()
}
