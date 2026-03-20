package factsource

import (
	"encoding/csv"
	"fmt"
	"os"
)

func init() {
	Register(".csv", LoaderFunc(loadCSV))
	RegisterSaver(".csv", SaverFunc(saveCSV))
}

// loadCSV reads a CSV file where:
//   - First row is headers
//   - Required columns: "type" and "key"
//   - All other columns become Fields
//   - Lines starting with # are skipped (comments)
//   - Numeric strings are auto-converted to float64
//   - "true"/"false" are auto-converted to bool
//   - Empty cells are omitted from Fields
func loadCSV(path string) ([]Fact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.Comment = '#'
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}
	return factsFromRows("csv", records)
}

func saveCSV(path string, facts []Fact) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("csv: %w", err)
	}
	defer f.Close()

	writer := csv.NewWriter(f)
	if err := writer.WriteAll(factsToRows(facts)); err != nil {
		return fmt.Errorf("csv write: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("csv flush: %w", err)
	}
	return nil
}
