package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/dadav/helm-schema/pkg/chart"
	"github.com/dadav/helm-schema/pkg/schema"
	"github.com/dadav/helm-schema/pkg/util"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	yaml "gopkg.in/yaml.v3"
)

func searchFiles(startPath, fileName string, queue chan<- string, errs chan<- error) {
	defer close(queue)
	err := filepath.Walk(startPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			errs <- err
			return nil
		}

		if !info.IsDir() && info.Name() == fileName {
			queue <- path
		}

		return nil
	})

	if err != nil {
		errs <- err
	}
}

type Result struct {
	ChartPath  string
	ValuesPath string
	Chart      *chart.ChartFile
	Schema     map[string]interface{}
	Errors     []error
}

func worker(
	dryRun, skipDeps, useRef, keepFullComment bool,
	valueFileNames []string,
	outFile string,
	queue <-chan string,
	results chan<- Result,
) {
	for chartPath := range queue {
		result := Result{ChartPath: chartPath}

		chartBasePath := filepath.Dir(chartPath)
		file, err := os.Open(chartPath)
		if err != nil {
			result.Errors = append(result.Errors, err)
			results <- result
			continue
		}

		chart, err := chart.ReadChart(file)
		if err != nil {
			result.Errors = append(result.Errors, err)
			results <- result
			continue
		}
		result.Chart = &chart

		var valuesPath string
		var valuesFound bool
		errorsWeMaybeCanIgnore := []error{}

		for _, possibleValueFileName := range valueFileNames {
			valuesPath = filepath.Join(chartBasePath, possibleValueFileName)
			_, err := os.Stat(valuesPath)
			if err != nil {
				if !os.IsNotExist(err) {
					errorsWeMaybeCanIgnore = append(errorsWeMaybeCanIgnore, err)
				}
				continue
			}
			valuesFound = true
			break
		}

		if !valuesFound {
			for _, err := range errorsWeMaybeCanIgnore {
				result.Errors = append(result.Errors, err)
			}
			result.Errors = append(result.Errors, errors.New("No values file found."))
			results <- result
			continue
		}
		result.ValuesPath = valuesPath

		valuesFile, err := os.Open(valuesPath)
		if err != nil {
			result.Errors = append(result.Errors, err)
			results <- result
			continue
		}
		content, err := util.ReadFileAndFixNewline(valuesFile)
		if err != nil {
			result.Errors = append(result.Errors, err)
			results <- result
			continue
		}

		var values yaml.Node
		err = yaml.Unmarshal(content, &values)
		if err != nil {
			result.Errors = append(result.Errors, err)
			results <- result
			continue
		}

		mainSchema := schema.YamlToJsonSchema(&values, keepFullComment, nil)
		result.Schema = mainSchema

		if !skipDeps {
			for _, dep := range chart.Dependencies {
				if depName, ok := dep["name"].(string); ok {
					if useRef {
						mainSchema["properties"].(map[string]interface{})[depName] = map[string]string{
							"title":       chart.Name,
							"description": chart.Description,
							"$ref":        fmt.Sprintf("charts/%s/%s", depName, outFile),
						}
					}
				}
			}
		}

		results <- result
	}
}

func exec(_ *cobra.Command, _ []string) {
	configureLogging()

	chartSearchRoot := viper.GetString("chart-search-root")
	dryRun := viper.GetBool("dry-run")
	useRef := viper.GetBool("use-references")
	noDeps := viper.GetBool("no-dependencies")
	keepFullComment := viper.GetBool("keep-full-comment")
	outFile := viper.GetString("output-file")
	valueFileNames := viper.GetStringSlice("value-files")
	workersCount := runtime.NumCPU() * 2

	// 1. Start a producer that searches Chart.yaml and values.yaml files
	queue := make(chan string)
	resultsChan := make(chan Result)
	results := []Result{}
	errs := make(chan error)
	done := make(chan struct{})

	go searchFiles(chartSearchRoot, "Chart.yaml", queue, errs)

	// 2. Start workers and every worker does:
	wg := sync.WaitGroup{}
	go func() {
		wg.Wait()
		done <- struct{}{}
	}()

	for i := 0; i < workersCount; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()
			worker(
				dryRun,
				noDeps,
				useRef,
				keepFullComment,
				valueFileNames,
				outFile,
				queue,
				resultsChan,
			)
		}()
	}

loop:
	for {
		select {
		case err := <-errs:
			log.Error(err)
		case res := <-resultsChan:
			results = append(results, res)
		case <-done:
			break loop

		}
	}

	// Sort results if dependencies should be processed
	// Need to resolve the dependencies from deepest level to highest
	if !noDeps && !useRef {
		sort.Slice(results, func(i, j int) bool {
			first := results[i]
			second := results[j]

			// No dependencies
			if len(first.Chart.Dependencies) == 0 {
				return true
			}
			// First is dependency of second
			for _, dep := range second.Chart.Dependencies {
				if name, ok := dep["name"]; ok {
					if name == first.Chart.Name {
						return true
					}
				}
			}

			// first comes after second
			return false
		})
	}

	chartNameToResult := make(map[string]Result)

	// process results
	for _, result := range results {
		// Error handling
		if len(result.Errors) > 0 {
			if result.Chart != nil {
				log.Errorf(
					"Found %d errors while processing the chart %s (%s)",
					len(result.Errors),
					result.Chart.Name,
					result.ChartPath,
				)
			} else {
				log.Errorf("Found %d errors while processing the chart %s", len(result.Errors), result.ChartPath)
			}
			for _, err := range result.Errors {
				log.Error(err)
			}
			continue
		}

		// Embed dependencies if needed
		if !noDeps && !useRef {
			for _, dep := range result.Chart.Dependencies {
				if depName, ok := dep["name"].(string); ok {
					if dependencyResult, ok := chartNameToResult[depName]; ok {
						result.Schema["properties"].(map[string]interface{})[depName] = map[string]interface{}{
							"type":        "object",
							"title":       depName,
							"description": dependencyResult.Chart.Description,
							"properties":  dependencyResult.Schema["properties"],
						}
					}
				}
			}
			chartNameToResult[result.Chart.Name] = result
		}

		// Print to stdout or write to file
		jsonStr, err := json.MarshalIndent(result.Schema, "", "  ")
		if err != nil {
			log.Error(err)
			continue
		}

		if dryRun {
			log.Infof("Printing jsonschema for %s chart (%s)", result.Chart.Name, result.ChartPath)
			fmt.Printf("%s\n", jsonStr)
		} else {
			chartBasePath := filepath.Dir(result.ChartPath)
			if err := os.WriteFile(filepath.Join(chartBasePath, outFile), jsonStr, 0644); err != nil {
				errs <- err
				continue
			}
		}
	}

}

func main() {
	command, err := newCommand(exec)
	if err != nil {
		log.Errorf("Failed to create the CLI commander: %s", err)
		os.Exit(1)
	}

	if err := command.Execute(); err != nil {
		log.Errorf("Failed to start the CLI: %s", err)
		os.Exit(1)
	}
}
