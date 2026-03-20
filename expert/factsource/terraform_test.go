package factsource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTerraformHCLResourceFacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	source := `resource "aws_instance" "web" {
  ami = "ami-123"
  instance_type = "t3.micro"
  count = 2
  associate_public_ip_address = true
  tags = {
    Name = "web"
  }
  ingress {
    from_port = 80
  }
}

data "aws_ami" "ubuntu" {
  most_recent = true
}
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	facts, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	resource := requireFact(t, facts, "Resource", "aws_instance.web")
	if resource.Fields["type"] != "aws_instance" || resource.Fields["name"] != "web" {
		t.Fatalf("resource identity = %+v", resource.Fields)
	}
	if resource.Fields["mode"] != "managed" {
		t.Fatalf("resource mode = %+v", resource.Fields["mode"])
	}
	if resource.Fields["count"] != float64(2) {
		t.Fatalf("count = %+v", resource.Fields["count"])
	}
	if resource.Fields["associate_public_ip_address"] != true {
		t.Fatalf("associate_public_ip_address = %+v", resource.Fields["associate_public_ip_address"])
	}
	typed := requireFact(t, facts, "aws_instance", "aws_instance.web")
	if typed.Fields["address"] != "aws_instance.web" || typed.Fields["resource_type"] != "aws_instance" {
		t.Fatalf("typed resource = %+v", typed.Fields)
	}
	tags, ok := resource.Fields["tags"].(map[string]any)
	if !ok || tags["Name"] != "web" {
		t.Fatalf("tags = %+v", resource.Fields["tags"])
	}
	ingress, ok := resource.Fields["ingress"].([]any)
	if !ok || len(ingress) != 1 {
		t.Fatalf("ingress = %+v", resource.Fields["ingress"])
	}
	entry, ok := ingress[0].(map[string]any)
	if !ok || entry["from_port"] != float64(80) {
		t.Fatalf("ingress[0] = %+v", ingress[0])
	}

	data := requireFact(t, facts, "Resource", "data.aws_ami.ubuntu")
	if data.Fields["mode"] != "data" || data.Fields["most_recent"] != true {
		t.Fatalf("data resource = %+v", data.Fields)
	}
}

func TestLoadTerraformDirectoryIncludesTFVars(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`module "network" { source = "./modules/network" }`), 0o644); err != nil {
		t.Fatalf("WriteFile main.tf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(`env = "prod"`), 0o644); err != nil {
		t.Fatalf("WriteFile terraform.tfvars: %v", err)
	}

	facts, err := Load("terraform://" + dir)
	if err != nil {
		t.Fatalf("Load dir: %v", err)
	}

	module := requireFact(t, facts, "Module", "network")
	if module.Fields["source"] != "./modules/network" {
		t.Fatalf("module = %+v", module.Fields)
	}
	variable := requireFact(t, facts, "VariableValue", "env")
	if variable.Fields["value"] != "prod" {
		t.Fatalf("variable = %+v", variable.Fields)
	}
}

func TestLoadTerraformPlanJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	payload, err := json.Marshal(terraformPlan{
		PlannedValues: terraformPlanValues{
			RootModule: &terraformPlanModule{
				Resources: []terraformPlanResource{{
					Address:      "aws_s3_bucket.assets",
					Mode:         "managed",
					Type:         "aws_s3_bucket",
					Name:         "assets",
					ProviderName: "registry.terraform.io/hashicorp/aws",
					Values: map[string]any{
						"acl":    "public-read",
						"bucket": "assets-prod",
					},
				}},
			},
		},
		ResourceChanges: []terraformPlanResourceChange{{
			Address: "aws_s3_bucket.assets",
			Mode:    "managed",
			Type:    "aws_s3_bucket",
			Name:    "assets",
			Change: terraformPlanChange{
				Actions: []string{"delete", "create"},
				Before: map[string]any{
					"acl": "private",
				},
				After: map[string]any{
					"acl": "public-read",
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	facts, err := Load("terraform:///" + filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("Load plan: %v", err)
	}

	resource := requireFact(t, facts, "Resource", "aws_s3_bucket.assets")
	if resource.Fields["acl"] != "public-read" || resource.Fields["bucket"] != "assets-prod" {
		t.Fatalf("resource = %+v", resource.Fields)
	}
	if resource.Fields["provider_name"] != "registry.terraform.io/hashicorp/aws" {
		t.Fatalf("provider_name = %+v", resource.Fields["provider_name"])
	}
	typed := requireFact(t, facts, "aws_s3_bucket", "aws_s3_bucket.assets")
	if typed.Fields["address"] != "aws_s3_bucket.assets" {
		t.Fatalf("typed = %+v", typed.Fields)
	}
	change := requireFact(t, facts, "ResourceChange", "aws_s3_bucket.assets")
	if change.Fields["action_summary"] != "delete+create" || change.Fields["replace"] != true {
		t.Fatalf("change = %+v", change.Fields)
	}
	after, ok := change.Fields["after"].(map[string]any)
	if !ok || after["acl"] != "public-read" {
		t.Fatalf("change after = %+v", change.Fields["after"])
	}
}

func TestLoadTerraformRejectsMalformedResourceBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.tf")
	if err := os.WriteFile(path, []byte(`resource "aws_instance" { ami = "ami-123" }`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(path)
	if err == nil || err.Error() != "terraform hcl: resource block requires type and name labels" {
		t.Fatalf("expected label error, got %v", err)
	}
}

func requireFact(t *testing.T, facts []Fact, typ, key string) Fact {
	t.Helper()
	for _, fact := range facts {
		if fact.Type == typ && fact.Key == key {
			return fact
		}
	}
	t.Fatalf("missing fact %s/%s in %+v", typ, key, facts)
	return Fact{}
}
