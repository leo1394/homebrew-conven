package examples

import _ "embed"

type WorkspaceFile struct {
	Name string
	Data []byte
}

//go:embed application.yaml
var ApplicationYAML []byte

//go:embed workspace/services.properties
var ServicesProperties []byte

//go:embed workspace/disabled-services.properties
var DisabledServicesProperties []byte

//go:embed workspace/CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md
var WorkspacePolicyGeneratorAISpec []byte

//go:embed workspace/CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC-EN.md
var WorkspacePolicyGeneratorAISpecEnglish []byte

//go:embed workspace/README.md
var WorkspaceREADME []byte

func WorkspaceFiles() []WorkspaceFile {
	return WorkspaceFilesForPolicySpecification(WorkspacePolicyGeneratorAISpec)
}

func WorkspaceFilesForPolicySpecification(specification []byte) []WorkspaceFile {
	return []WorkspaceFile{
		{Name: "services.properties", Data: ServicesProperties},
		{Name: "disabled-services.properties", Data: DisabledServicesProperties},
		{Name: "CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md", Data: specification},
		{Name: "README.md", Data: WorkspaceREADME},
	}
}
