package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var ocrReport = flag.String("ocr-report", "", "write measured Go/original-Python OCR corpus results to this JSON file")

type ocrCorpusCase struct {
	Name       string `json:"name"`
	JPEG       string `json:"jpeg"`
	PythonPGM  string `json:"python_pgm"`
	Expected   string `json:"expected"`
	PythonText string `json:"original_python_text"`
}

func runeEditDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	previous := make([]int, len(y)+1)
	for i := range previous {
		previous[i] = i
	}
	for i, ar := range x {
		current := make([]int, len(y)+1)
		current[0] = i + 1
		for j, br := range y {
			cost := 0
			if ar != br {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(y)]
}

func TestOCRCorpus(t *testing.T) {
	executable, err := resolveTesseract("")
	if err != nil {
		t.Skip("Tesseract is optional:", err)
	}
	const directory = "testdata/ocr"
	data, err := os.ReadFile(filepath.Join(directory, "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus []ocrCorpusCase
	if err := json.Unmarshal(data, &corpus); err != nil || len(corpus) != 4 {
		t.Fatalf("corpus=%d,error%v", len(corpus), err)
	}
	type result struct {
		Name, GoText, PythonText, Expected string
		GoErrors, PythonErrors             int
	}
	var results []result
	for _, sample := range corpus {
		t.Run(sample.Name, func(t *testing.T) {
			jpeg, err := os.ReadFile(filepath.Join(directory, sample.JPEG))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := extractOCR(t.Context(), executable, jpeg)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := os.ReadFile(filepath.Join(directory, sample.PythonPGM))
			if err != nil {
				t.Fatal(err)
			}
			text, err := runTesseract(t.Context(), executable, baseline, ocrTimeout)
			if err != nil {
				t.Fatal(err)
			}
			python := postprocessOCR(text)
			goErrors := runeEditDistance(sample.Expected, actual)
			pythonErrors := runeEditDistance(sample.Expected, python)
			t.Logf("Go%d / Python%d character edits; expected%d runes\nGo: %q\nPython: %q", goErrors, pythonErrors, len([]rune(sample.Expected)), actual, python)
			if goErrors > pythonErrors {
				t.Errorf("Go preprocessing is less accurate than original Python: %d vs%d edits", goErrors, pythonErrors)
			}
			results = append(results, result{sample.Name, actual, python, sample.Expected, goErrors, pythonErrors})
		})
	}
	if *ocrReport != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(*ocrReport, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
