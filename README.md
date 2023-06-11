# \[G\]o\[PT\]est
Just don't ask about the name
## Installation
`go install github.com/sentiens/goptest@latest`

## Usage
Only GPT4 is supported. TODO: More configuration options.

1. Set OPENAI_API_KEY environment variable
1. First generate specification for the tested code:
```goptest -cases=true -what="Add function" -spec-file=specs.yaml -code-files=testcode.go```

2. Review and edit `specs.yml` code
3. Run to generate tests code 
```goptest -spec-file=specs.yaml -code-files=testcode.go -output-file=generated_test.go``` 

