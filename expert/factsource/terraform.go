package factsource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func init() {
	Register(".tf", LoaderFunc(loadTerraformHCL))
	Register(".tfvars", LoaderFunc(loadTerraformHCL))
	Register(".hcl", LoaderFunc(loadTerraformHCL))
	Register("terraform://", LoaderFunc(loadTerraform))
}

type terraformPlan struct {
	PlannedValues   terraformPlanValues           `json:"planned_values"`
	ResourceChanges []terraformPlanResourceChange `json:"resource_changes"`
}

type terraformPlanValues struct {
	RootModule *terraformPlanModule `json:"root_module"`
}

type terraformPlanModule struct {
	Resources    []terraformPlanResource `json:"resources"`
	ChildModules []terraformPlanModule   `json:"child_modules"`
}

type terraformPlanResource struct {
	Address      string         `json:"address"`
	Mode         string         `json:"mode"`
	Type         string         `json:"type"`
	Name         string         `json:"name"`
	ProviderName string         `json:"provider_name"`
	Values       map[string]any `json:"values"`
}

type terraformPlanResourceChange struct {
	Address      string              `json:"address"`
	Mode         string              `json:"mode"`
	Type         string              `json:"type"`
	Name         string              `json:"name"`
	ProviderName string              `json:"provider_name"`
	Change       terraformPlanChange `json:"change"`
}

type terraformPlanChange struct {
	Actions         []string `json:"actions"`
	Before          any      `json:"before"`
	After           any      `json:"after"`
	AfterUnknown    any      `json:"after_unknown"`
	BeforeSensitive any      `json:"before_sensitive"`
	AfterSensitive  any      `json:"after_sensitive"`
}

func loadTerraform(path string) ([]Fact, error) {
	target, err := terraformTargetPath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	if info.IsDir() {
		return loadTerraformDirectory(target)
	}
	if strings.EqualFold(filepath.Ext(target), ".json") {
		return loadTerraformPlan(target)
	}
	return loadTerraformHCL(target)
}

func loadTerraformHCL(path string) ([]Fact, error) {
	entry := grammars.DetectLanguage(path)
	if entry == nil || entry.Name != "hcl" {
		return nil, fmt.Errorf("terraform hcl: no HCL grammar for %q", path)
	}

	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("terraform hcl: %w", err)
	}
	parser := gotreesitter.NewParser(entry.Language())
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("terraform hcl parse: %w", err)
	}
	root := tree.RootNode()
	if root == nil {
		return nil, fmt.Errorf("terraform hcl: empty parse tree for %q", path)
	}
	if root.HasError() {
		return nil, fmt.Errorf("terraform hcl: parse errors in %q", path)
	}
	facts, err := extractTerraformHCLFacts(root, source, tree.Language(), path)
	if err != nil {
		return nil, err
	}
	return facts, nil
}

func loadTerraformDirectory(dir string) ([]Fact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".tf" && ext != ".tfvars" && ext != ".hcl" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	slices.Sort(paths)

	out := make([]Fact, 0)
	for _, path := range paths {
		facts, err := loadTerraformHCL(path)
		if err != nil {
			return nil, err
		}
		out = append(out, facts...)
	}
	return out, nil
}

func loadTerraformPlan(path string) ([]Fact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("terraform plan: %w", err)
	}

	var plan terraformPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("terraform plan parse: %w", err)
	}
	if plan.PlannedValues.RootModule == nil {
		return nil, nil
	}
	out := make([]Fact, 0)
	collectTerraformPlanFacts(plan.PlannedValues.RootModule, path, &out)
	collectTerraformPlanChangeFacts(plan.ResourceChanges, path, &out)
	return out, nil
}

func collectTerraformPlanFacts(module *terraformPlanModule, sourcePath string, out *[]Fact) {
	if module == nil {
		return
	}
	for _, resource := range module.Resources {
		fields := cloneFactFields(resource.Values)
		if fields == nil {
			fields = map[string]any{}
		}
		fields["type"] = resource.Type
		fields["name"] = resource.Name
		fields["address"] = resource.Address
		fields["mode"] = firstNonEmptyString(resource.Mode, "managed")
		if resource.ProviderName != "" {
			fields["provider_name"] = resource.ProviderName
		}
		fields["kind"] = firstNonEmptyString(resource.Mode, "managed")
		fields["source_path"] = sourcePath
		*out = append(*out, terraformResourceFacts(resource.Type, resource.Address, fields)...)
	}
	for i := range module.ChildModules {
		collectTerraformPlanFacts(&module.ChildModules[i], sourcePath, out)
	}
}

func collectTerraformPlanChangeFacts(changes []terraformPlanResourceChange, sourcePath string, out *[]Fact) {
	for _, change := range changes {
		fields := map[string]any{
			"type":           change.Type,
			"resource_type":  change.Type,
			"name":           change.Name,
			"address":        change.Address,
			"mode":           firstNonEmptyString(change.Mode, "managed"),
			"actions":        append([]string(nil), change.Change.Actions...),
			"action_summary": terraformActionSummary(change.Change.Actions),
			"replace":        terraformActionsContain(change.Change.Actions, "delete") && terraformActionsContain(change.Change.Actions, "create"),
			"source_path":    sourcePath,
		}
		if change.ProviderName != "" {
			fields["provider_name"] = change.ProviderName
		}
		if change.Change.Before != nil {
			fields["before"] = change.Change.Before
		}
		if change.Change.After != nil {
			fields["after"] = change.Change.After
		}
		if change.Change.AfterUnknown != nil {
			fields["after_unknown"] = change.Change.AfterUnknown
		}
		if change.Change.BeforeSensitive != nil {
			fields["before_sensitive"] = change.Change.BeforeSensitive
		}
		if change.Change.AfterSensitive != nil {
			fields["after_sensitive"] = change.Change.AfterSensitive
		}
		*out = append(*out, Fact{
			Type:   "ResourceChange",
			Key:    change.Address,
			Fields: fields,
		})
	}
}

func extractTerraformHCLFacts(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) ([]Fact, error) {
	body := firstNamedChildOfType(root, lang, "body")
	if body == nil {
		return nil, nil
	}
	out := make([]Fact, 0)
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		switch child.Type(lang) {
		case "block":
			facts, err := terraformFactsFromBlock(child, source, lang, sourcePath)
			if err != nil {
				return nil, err
			}
			out = append(out, facts...)
		case "attribute":
			fact, err := terraformFactFromTopLevelAttribute(child, source, lang, sourcePath)
			if err != nil {
				return nil, err
			}
			if fact.Type != "" {
				out = append(out, fact)
			}
		}
	}
	return out, nil
}

func terraformFactsFromBlock(block *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) ([]Fact, error) {
	blockType, labels, body, err := parseTerraformBlockHeader(block, source, lang)
	if err != nil {
		return nil, err
	}

	switch blockType {
	case "locals":
		return terraformLocalFacts(body, source, lang, sourcePath)
	case "resource":
		facts, err := terraformManagedResourceFacts(labels, body, source, lang, sourcePath)
		if err != nil {
			return nil, err
		}
		return facts, nil
	case "data":
		facts, err := terraformDataResourceFacts(labels, body, source, lang, sourcePath)
		if err != nil {
			return nil, err
		}
		return facts, nil
	case "module":
		return []Fact{terraformNamedBlockFact("Module", blockType, labels, body, source, lang, sourcePath)}, nil
	case "provider":
		return []Fact{terraformNamedBlockFact("Provider", blockType, labels, body, source, lang, sourcePath)}, nil
	case "variable":
		return []Fact{terraformNamedBlockFact("Variable", blockType, labels, body, source, lang, sourcePath)}, nil
	case "output":
		return []Fact{terraformNamedBlockFact("Output", blockType, labels, body, source, lang, sourcePath)}, nil
	case "terraform":
		return []Fact{terraformNamedBlockFact("Terraform", blockType, labels, body, source, lang, sourcePath)}, nil
	default:
		return []Fact{terraformNamedBlockFact("Block", blockType, labels, body, source, lang, sourcePath)}, nil
	}
}

func terraformManagedResourceFacts(labels []string, body *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) ([]Fact, error) {
	if len(labels) < 2 {
		return nil, fmt.Errorf("terraform hcl: resource block requires type and name labels")
	}
	fields, _ := decodeTerraformBody(body, source, lang)
	if fields == nil {
		fields = map[string]any{}
	}
	resourceType, name := labelAt(labels, 0), labelAt(labels, 1)
	address := resourceType + "." + name
	fields["type"] = resourceType
	fields["resource_type"] = resourceType
	fields["name"] = name
	fields["address"] = address
	fields["mode"] = "managed"
	fields["kind"] = "managed"
	fields["source_path"] = sourcePath
	return terraformResourceFacts(resourceType, address, fields), nil
}

func terraformDataResourceFacts(labels []string, body *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) ([]Fact, error) {
	if len(labels) < 2 {
		return nil, fmt.Errorf("terraform hcl: data block requires type and name labels")
	}
	fields, _ := decodeTerraformBody(body, source, lang)
	if fields == nil {
		fields = map[string]any{}
	}
	resourceType, name := labelAt(labels, 0), labelAt(labels, 1)
	address := "data." + resourceType + "." + name
	fields["type"] = resourceType
	fields["resource_type"] = resourceType
	fields["name"] = name
	fields["address"] = address
	fields["mode"] = "data"
	fields["kind"] = "data"
	fields["source_path"] = sourcePath
	return terraformResourceFacts(resourceType, address, fields), nil
}

func terraformNamedBlockFact(factType, blockType string, labels []string, body *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) Fact {
	fields, _ := decodeTerraformBody(body, source, lang)
	if fields == nil {
		fields = map[string]any{}
	}
	key := blockType
	if len(labels) > 0 {
		key = labels[0]
		fields["labels"] = append([]string(nil), labels...)
	}
	fields["block_type"] = blockType
	fields["source_path"] = sourcePath
	return Fact{
		Type:   factType,
		Key:    key,
		Fields: fields,
	}
}

func terraformLocalFacts(body *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) ([]Fact, error) {
	if body == nil {
		return nil, nil
	}
	out := make([]Fact, 0)
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if child.Type(lang) != "attribute" {
			continue
		}
		name, value, err := decodeTerraformAttribute(child, source, lang)
		if err != nil {
			return nil, err
		}
		out = append(out, Fact{
			Type: "Local",
			Key:  name,
			Fields: map[string]any{
				"value":       value,
				"source_path": sourcePath,
			},
		})
	}
	return out, nil
}

func terraformResourceFacts(resourceType, address string, fields map[string]any) []Fact {
	generic := Fact{
		Type:   "Resource",
		Key:    address,
		Fields: cloneFactFields(fields),
	}
	typed := Fact{
		Type:   terraformTypeFactName(resourceType),
		Key:    address,
		Fields: cloneFactFields(fields),
	}
	return []Fact{generic, typed}
}

func terraformTypeFactName(resourceType string) string {
	if resourceType == "" {
		return "ResourceType"
	}
	var b strings.Builder
	for i, r := range resourceType {
		switch {
		case r == '_' || (r >= '0' && r <= '9' && i > 0) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func terraformActionSummary(actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	return strings.Join(actions, "+")
}

func terraformActionsContain(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

func terraformFactFromTopLevelAttribute(attribute *gotreesitter.Node, source []byte, lang *gotreesitter.Language, sourcePath string) (Fact, error) {
	name, value, err := decodeTerraformAttribute(attribute, source, lang)
	if err != nil {
		return Fact{}, err
	}
	return Fact{
		Type: "VariableValue",
		Key:  name,
		Fields: map[string]any{
			"value":       value,
			"source_path": sourcePath,
		},
	}, nil
}

func parseTerraformBlockHeader(block *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (string, []string, *gotreesitter.Node, error) {
	if block == nil {
		return "", nil, nil, fmt.Errorf("terraform hcl: nil block")
	}
	var (
		blockType string
		labels    []string
		body      *gotreesitter.Node
	)
	for i := 0; i < int(block.NamedChildCount()); i++ {
		child := block.NamedChild(i)
		switch child.Type(lang) {
		case "body":
			body = child
		case "identifier":
			text := strings.TrimSpace(child.Text(source))
			if blockType == "" {
				blockType = text
			} else {
				labels = append(labels, text)
			}
		case "string_lit":
			labels = append(labels, terraformLabelValue(child, source))
		}
	}
	if blockType == "" {
		return "", nil, nil, fmt.Errorf("terraform hcl: block missing type")
	}
	return blockType, labels, body, nil
}

func decodeTerraformBody(body *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (map[string]any, error) {
	if body == nil {
		return nil, nil
	}
	fields := make(map[string]any)
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		switch child.Type(lang) {
		case "attribute":
			name, value, err := decodeTerraformAttribute(child, source, lang)
			if err != nil {
				return nil, err
			}
			fields[name] = value
		case "block":
			blockType, labels, nestedBody, err := parseTerraformBlockHeader(child, source, lang)
			if err != nil {
				return nil, err
			}
			nestedFields, err := decodeTerraformBody(nestedBody, source, lang)
			if err != nil {
				return nil, err
			}
			if nestedFields == nil {
				nestedFields = map[string]any{}
			}
			if len(labels) > 0 {
				nestedFields["labels"] = append([]string(nil), labels...)
			}
			existing, _ := fields[blockType].([]any)
			fields[blockType] = append(existing, nestedFields)
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

func decodeTerraformAttribute(attribute *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (string, any, error) {
	if attribute == nil || attribute.NamedChildCount() == 0 {
		return "", nil, fmt.Errorf("terraform hcl: malformed attribute")
	}
	nameNode := attribute.NamedChild(0)
	valueNode := attribute.NamedChild(int(attribute.NamedChildCount() - 1))
	name := strings.TrimSpace(nameNode.Text(source))
	value, err := decodeTerraformValue(valueNode, source, lang)
	if err != nil {
		return "", nil, err
	}
	return name, value, nil
}

func decodeTerraformValue(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (any, error) {
	if node == nil {
		return nil, nil
	}
	switch node.Type(lang) {
	case "expression", "literal_value", "collection_value":
		if node.NamedChildCount() == 1 {
			return decodeTerraformValue(node.NamedChild(0), source, lang)
		}
		return strings.TrimSpace(node.Text(source)), nil
	case "string_lit":
		return terraformStringValue(node.Text(source)), nil
	case "numeric_lit":
		return strconv.ParseFloat(strings.TrimSpace(node.Text(source)), 64)
	case "bool_lit":
		return strings.TrimSpace(node.Text(source)) == "true", nil
	case "null_lit":
		return nil, nil
	case "tuple":
		values := make([]any, 0, node.NamedChildCount())
		for i := 0; i < int(node.NamedChildCount()); i++ {
			value, err := decodeTerraformValue(node.NamedChild(i), source, lang)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case "object":
		return decodeTerraformObject(node, source, lang)
	default:
		return strings.TrimSpace(node.Text(source)), nil
	}
}

func decodeTerraformObject(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (map[string]any, error) {
	out := make(map[string]any)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type(lang) != "object_elem" || child.NamedChildCount() < 2 {
			continue
		}
		keyValue, err := decodeTerraformValue(child.NamedChild(0), source, lang)
		if err != nil {
			return nil, err
		}
		value, err := decodeTerraformValue(child.NamedChild(1), source, lang)
		if err != nil {
			return nil, err
		}
		out[terraformObjectKey(keyValue)] = value
	}
	return out, nil
}

func terraformStringValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") && !strings.Contains(raw, "${") && !strings.Contains(raw, "%{") {
		if value, err := strconv.Unquote(raw); err == nil {
			return value
		}
	}
	return raw
}

func terraformObjectKey(v any) string {
	switch value := v.(type) {
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func terraformLabelValue(node *gotreesitter.Node, source []byte) string {
	return fmt.Sprint(terraformStringValue(node.Text(source)))
}

func terraformTargetPath(uri string) (string, error) {
	const prefix = "terraform://"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("terraform: unsupported uri %q", uri)
	}
	target := strings.TrimPrefix(uri, prefix)
	target = strings.TrimPrefix(target, "/")
	if strings.HasPrefix(uri, "terraform:///") {
		target = "/" + target
	}
	if target == "" {
		return "", fmt.Errorf("terraform: target path is required")
	}
	return filepath.Clean(target), nil
}

func firstNamedChildOfType(node *gotreesitter.Node, lang *gotreesitter.Language, want string) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type(lang) == want {
			return child
		}
	}
	return nil
}

func labelAt(labels []string, idx int) string {
	if idx < 0 || idx >= len(labels) {
		return ""
	}
	return labels[idx]
}
