package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v2"
)

// Spec represents a single test specification.
type Spec struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ConcatFiles combines multiple code files into a single string.
func ConcatFiles(fs []string) (pkgName string, files string, err error) {
	// TODO: Summarize methods and dependencies as signatures

	var cb strings.Builder

	for i, f := range fs {
		fc, err := os.ReadFile(f)
		if err != nil {
			return "", "", err
		}

		// prepend the package name to the first file content only
		if i == 0 {
			scanner := bufio.NewScanner(bytes.NewReader(fc))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "package ") {
					pkgName = strings.TrimPrefix(line, "package ")
					break
				}
			}
			if err := scanner.Err(); err != nil {
				return "", "", err
			}
		}

		cb.WriteString("// " + f + "\n")
		cb.Write(fc)
		cb.WriteString("\n")
	}

	cc := cb.String()
	return pkgName, cc, nil
}

// Client is a client for interacting with the OpenAI API.
type Client struct {
	client *openai.Client
}

// NewClient initializes a new OpenAI API client.
func NewClient() (*Client, error) {
	k := os.Getenv("OPENAI_API_KEY")
	if k == "" {
		return nil, errors.New("no OpenAI API key provided")
	}
	c := openai.NewClient(k)

	return &Client{c}, nil
}

func testCodeTemplate(name string) string {
	return fmt.Sprintf(`// %s tests the function with given conditions.
func %s(t *testing.T) {
	// The test code goes here
}
`, name, name)
}

func codeGenerationPrompt(whatToTest string, spec Spec, functionalityDesc string, allTheCode string) string {
	return fmt.Sprintf(
		"You are a highly skilled Go developer with a knack for identifying potential bugs and edge cases.\n"+
			"You have been tasked with implementing a test function designed by a TDD expert, but you believe in understanding and scrutinizing the task at hand rather than blindly following authority.\n"+
			"Here is the original code that needs to be tested: ```go\n%s```\n"+
			"The functionality you need to test is '%s' and here is a description of the functionality: ```%s```\n"+
			"Here is the specification of the test you should implement: ```%s```\n"+
			"Please use the following template for your output, replacing any placeholders and comments with your own. However, avoid repeating any original code:\n```go\n%s\n```\n"+
			"Your output should be valid Go test code, including any necessary comments."+
			"Begin generating the code now.\n",
		allTheCode,
		whatToTest,
		functionalityDesc,
		spec.Description,
		testCodeTemplate(spec.Name),
	)
}

// GenerateTestCode generates test code using the OpenAI chat completion API.
func (c *Client) GenerateTestCode(spec Spec, whatToTest string, codeDescription string, allCode string) (string, error) {
	//TODO: Implement retries to not blow up the whole process

	ctx := context.Background()

	content := codeGenerationPrompt(whatToTest, spec, codeDescription, allCode)

	msg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: content,
	}

	req := openai.ChatCompletionRequest{
		Model:       openai.GPT4,
		MaxTokens:   4000,
		Temperature: 0.8,

		Messages: []openai.ChatCompletionMessage{
			msg,
		},
	}

	resp, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}

const yamlExample = `code_description: >
  # Here, provide a detailed and precise explanation of the tested functionality, its invariants, and edge cases.
  # Enumerate the different classes of input values and their possible combinations.

specs:
  # Begin with the simplest case. Example:
  - 
    name: TestAdd
    description: "Specify the test description here"
  
  # If there are any error cases, describe them here. Example:
  - 
    name: TestAddOverflow
    description: "Specify the test description here"
  
  # Then describe all other test cases, including edge cases. Example:
  - 
    name: TestAddNegativeNumbers
    description: "Specify the test description here"
`

func promptForTestCase(whatToTest string, allCode string) []openai.ChatCompletionMessage {
	systemContent :=
		"As a TDD expert and QA analyst with advanced logic, analytics, and reasoning skills, you're tasked with generating test cases." +
			"You use a precise and consice software specifications vocabulary, accessible to a Language Model."

	systemMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemContent,
	}

	userContent := fmt.Sprintf(
		"Analyze the `%s` functionality in the following code: \n```go\n%s```\n"+
			"Generate test cases, focusing on the `%s` functionality. Use only YAML as a response format. \nTemplate: \n```yaml\n%s\n```\n"+
			"First, describe the tested functionality in the YAML `code_description` field. Enumerate what each element of the `%s` functionality does and the possible intentions of the author.\n"+
			"Remember to only use the `name` and `description` fields in each test case.",
		whatToTest,
		allCode,
		whatToTest,
		yamlExample,
		whatToTest,
	)
	userMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userContent,
	}
	fmt.Println("System message:", systemMsg)
	fmt.Println("User message:", userMsg)
	return []openai.ChatCompletionMessage{
		systemMsg,
		userMsg,
	}
}

func (c *Client) GenerateTestCases(whatToTest string, allCode string) (string, error) {
	//TODO: First generate just the text from multiple perspectives and merge it and then map it to yaml format
	ctx := context.Background()

	req := openai.ChatCompletionRequest{
		Model:       openai.GPT4,
		MaxTokens:   4000,
		Temperature: 1,

		Messages: promptForTestCase(whatToTest, allCode),
	}

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return "", err
	}
	var result string
	defer stream.Close()
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}

		if err != nil {
			return "", err
		}

		fmt.Printf(response.Choices[0].Delta.Content)
		result += response.Choices[0].Delta.Content
	}
}

// AggregateResponses combines responses into a single string, ensuring that the output is a valid Go tests file.
func AggregateResponses(pkgName string, rs []string) string {
	var imports strings.Builder
	var functions strings.Builder

	importSet := make(map[string]struct{})

	for _, response := range rs {
		lines := strings.Split(response, "\n")

		for i := 0; i < len(lines); i++ {
			line := lines[i]
			trimmedLine := strings.TrimSpace(line)

			switch {
			case isPackageDeclaration(trimmedLine), isGPTAddedCodeBlockDelimeter(trimmedLine):
				continue

			case startsImportBlock(trimmedLine):
				i += handleImportBlock(lines[i+1:], &imports, importSet)

			case isSingleImportStatement(trimmedLine):
				addImport(&imports, strings.TrimPrefix(trimmedLine, "import "), importSet)

			default:
				// Appending function bodies
				functions.WriteString(line + "\n")
			}
		}
		functions.WriteString("\n")
	}

	return combineSections("package "+pkgName, imports.String(), functions.String())
}

func isPackageDeclaration(line string) bool {
	return strings.HasPrefix(line, "package ")
}
func isGPTAddedCodeBlockDelimeter(line string) bool {
	return strings.HasPrefix(line, "```")
}

func startsImportBlock(line string) bool {
	return strings.HasPrefix(line, "import (")
}

func isSingleImportStatement(line string) bool {
	return strings.HasPrefix(line, "import \"")
}

func handleImportBlock(lines []string, imports *strings.Builder, importSet map[string]struct{}) int {
	for i, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, ")") {

			return i + 1
		}
		addImport(imports, trimmedLine, importSet)
	}
	panic("import block not closed")
}

func addImport(imports *strings.Builder, importLine string, importSet map[string]struct{}) {
	if _, exists := importSet[importLine]; !exists {
		imports.WriteString("\t" + importLine + "\n")
		importSet[importLine] = struct{}{}
	}
}

func combineSections(packageDecl, imports, functions string) string {
	var combined strings.Builder
	combined.WriteString(packageDecl)
	combined.WriteString("\n")
	if imports != "" {
		combined.WriteString("\nimport (\n")
		combined.WriteString(imports)
		combined.WriteString(")\n\n")
	}
	combined.WriteString(functions)
	return combined.String()
}

// WriteToFile writes the combined responses into a file.
func WriteToFile(out string, fPath string) error {
	file, err := os.OpenFile(fPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %v", err)
	}
	defer file.Close()

	_, err = file.WriteString(out)
	if err != nil {
		return fmt.Errorf("failed to write to file: %v", err)
	}

	err = file.Sync()
	if err != nil {
		return fmt.Errorf("failed to flush to disk: %v", err)
	}

	return nil
}

// SpecList wraps the array of Specs for unmarshalling from YAML
type SpecList struct {
	Testing         string `yaml:"testing"`
	CodeDescription string `yaml:"code_description"`
	Specs           []Spec `yaml:"specs"`
}

// LoadTestSpecs loads test specifications from a file.
func LoadTestSpecs(fPath string) (*SpecList, error) {
	file, err := os.Open(fPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var specList SpecList
	err = yaml.Unmarshal(content, &specList)
	if err != nil {
		return nil, err
	}

	return &specList, nil
}

func removeYamlLines(input string) string {
	lines := strings.Split(input, "\n")
	filtered := make([]string, 0, len(lines))

	for _, line := range lines {
		if !strings.Contains(line, "```yaml") && !strings.Contains(line, "```") {
			filtered = append(filtered, line)
		}
	}

	return strings.Join(filtered, "\n")
}

func main() {
	specFilePath := flag.String("spec-file", "", "Path to the spec file")
	codeFiles := flag.String("code-files", "", "Comma-separated paths to code files")
	outputFilePath := flag.String("output-file", "", "Path to output file")
	cases := flag.Bool("cases", false, "Generate cases or not, default false")
	whatToTest := flag.String("what", "", "What to test")
	flag.Parse()

	if *specFilePath == "" || *codeFiles == "" {
		log.Fatalf("spec-file, code-files, and output-file must be provided")
	}

	codeFilePaths := strings.Split(*codeFiles, ",")
	pkgName, concatenatedCode, err := ConcatFiles(codeFilePaths)
	if err != nil {
		log.Fatalf("Failed to concatenate code files: %v", err)
	}

	apiClient, err := NewClient()
	if err != nil {
		log.Fatalf("Failed to initialize OpenAI API client: %v", err)
	}
	if cases != nil && *cases {
		if whatToTest != nil && *whatToTest == "" {
			log.Fatalf("Must provide what to test")
		}
		fmt.Println("Generating test cases")
		s, err := apiClient.GenerateTestCases(*whatToTest, concatenatedCode)
		if err != nil {
			log.Fatalf("Failed to generate test cases: %v", err)
		}
		s = "testing: " + *whatToTest + "\n" + removeYamlLines(s)
		err = WriteToFile(s, *specFilePath)
		if err != nil {
			log.Fatalf("Failed to write test cases to file: %v", err)
		}
		fmt.Println("Done generating test cases")
		fmt.Printf("Test cases written to %s\n", *specFilePath)
		fmt.Println("Command to generate test code:")
		fmt.Print("tester -spec-file=" + *specFilePath + " -code-files=" + *codeFiles + " -output-file=" + "generated_test.go")
		return
	}

	if *outputFilePath == "" {
		log.Fatalf("Must provide output file path")
	}

	specs, err := LoadTestSpecs(*specFilePath)
	if err != nil {
		log.Fatalf("Failed to load test specs: %v", err)
	}

	responses := make([]string, len(specs.Specs))
	var wg sync.WaitGroup
	var max = make(chan struct{}, 4)
	for i, spec := range specs.Specs {
		max <- struct{}{}
		wg.Add(1)
		fmt.Printf("Generating test code %d of %d for spec '%s'\n", i+1, len(specs.Specs), spec.Description)
		go func(i int, spec Spec) {
			defer wg.Done()
			defer func() {
				<-max
			}()
			code, err := apiClient.GenerateTestCode(spec, specs.Testing, specs.CodeDescription, concatenatedCode)
			if err != nil {
				log.Fatalf("Failed to generate test code for spec '%s': %v", spec.Description, err)
			}
			responses[i] = code
			fmt.Println("Done generating test")
		}(i, spec)
	}

	wg.Wait()

	combinedCode := AggregateResponses(pkgName, responses)

	err = WriteToFile(combinedCode, *outputFilePath)
	if err != nil {
		log.Fatalf("Failed to write output to file: %v", err)
	}

	fmt.Println("Test generation succeeded. Check the output file for the generated test code.")
	fmt.Printf(*outputFilePath)
	fmt.Println()
}
