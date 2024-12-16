package schema

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/norwoodj/helm-docs/pkg/helm"
	"github.com/santhosh-tekuri/jsonschema/v5"
	log "github.com/sirupsen/logrus"
	"github.com/winterRel/helm-schema/pkg/jsonpointer"
	"github.com/winterRel/helm-schema/pkg/util"
	"gopkg.in/yaml.v3"
)

const (
	SchemaPrefix  = "# @schema"
	CommentPrefix = "#"

	// CustomAnnotationPrefix marks custom annotations.
	// custom annotations is a map of custom annotations. See introduction of custom annotation: https://json-schema.org/blog/posts/custom-annotations-will-continue
	CustomAnnotationPrefix = "x-"
)

const (
	nullTag      = "!!null"
	boolTag      = "!!bool"
	strTag       = "!!str"
	intTag       = "!!int"
	floatTag     = "!!float"
	timestampTag = "!!timestamp"
	arrayTag     = "!!seq"
	mapTag       = "!!map"
)

type SchemaOrBool interface{}

type BoolOrArrayOfString struct {
	Strings []string
	Bool    bool
}

func NewBoolOrArrayOfString(arr []string, b bool) BoolOrArrayOfString {
	return BoolOrArrayOfString{
		Strings: arr,
		Bool:    b,
	}
}

func (s *BoolOrArrayOfString) UnmarshalJSON(value []byte) error {
	var multi []string
	var single bool

	if err := json.Unmarshal(value, &multi); err == nil {
		s.Strings = multi
	} else if err := json.Unmarshal(value, &single); err == nil {
		s.Bool = single
	}
	return nil
}

func (s *BoolOrArrayOfString) MarshalJSON() ([]byte, error) {
	if s.Strings == nil {
		return json.Marshal([]string{})
	}
	return json.Marshal(s.Strings)
}

func (s *BoolOrArrayOfString) UnmarshalYAML(value *yaml.Node) error {
	var multi []string
	if value.ShortTag() == arrayTag {
		for _, v := range value.Content {
			var typeStr string
			err := v.Decode(&typeStr)
			if err != nil {
				return err
			}
			multi = append(multi, typeStr)
		}
		s.Strings = multi
	} else if value.ShortTag() == boolTag {
		var single bool
		err := value.Decode(&single)
		if err != nil {
			return err
		}
		s.Bool = single
	} else {
		return fmt.Errorf("could not unmarshal %v to slice of string or bool", value.Content)
	}
	return nil
}

type StringOrArrayOfString []string

func (s *StringOrArrayOfString) UnmarshalYAML(value *yaml.Node) error {
	var multi []string
	if value.ShortTag() == arrayTag {
		for _, v := range value.Content {
			if v.ShortTag() == nullTag {
				multi = append(multi, "null")
			} else {
				var typeStr string
				err := v.Decode(&typeStr)
				if err != nil {
					return err
				}
				multi = append(multi, typeStr)
			}
		}
		*s = multi
	} else {
		var single string
		err := value.Decode(&single)
		if err != nil {
			return err
		}
		*s = []string{single}
	}
	return nil
}

func (s *StringOrArrayOfString) UnmarshalJSON(value []byte) error {
	var multi []string
	var single string

	if err := json.Unmarshal(value, &multi); err == nil {
		*s = multi
	} else if err := json.Unmarshal(value, &single); err == nil {
		*s = []string{single}
	}
	return nil
}

func (s *StringOrArrayOfString) MarshalJSON() ([]byte, error) {
	if len(*s) == 1 {
		return json.Marshal([]string(*s)[0])
	}
	return json.Marshal([]string(*s))
}

func (s *StringOrArrayOfString) Validate() error {
	// Check if type is valid
	for _, t := range []string(*s) {
		if t != "" &&
			t != "object" &&
			t != "string" &&
			t != "integer" &&
			t != "number" &&
			t != "array" &&
			t != "null" &&
			t != "boolean" {
			return fmt.Errorf("unsupported type %s", s)
		}
	}
	return nil
}

func (s *StringOrArrayOfString) IsEmpty() bool {
	for _, t := range []string(*s) {
		if t == "" {
			return true
		}
	}
	return len(*s) == 0
}

func (s *StringOrArrayOfString) Matches(typeString string) bool {
	for _, t := range []string(*s) {
		if t == typeString {
			return true
		}
	}
	return false
}

// MarshalJSON custom marshal method for Schema. It inlines the CustomAnnotations fields
func (s *Schema) MarshalJSON() ([]byte, error) {
	// Create a map to hold all the fields
	type Alias Schema
	data := make(map[string]interface{})

	// Marshal the Schema struct (excluding CustomAnnotations)
	alias := (*Alias)(s)
	aliasJSON, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}

	// Unmarshal the JSON back into the map
	if err := json.Unmarshal(aliasJSON, &data); err != nil {
		return nil, err
	}

	// inline the CustomAnnotations fields
	for key, value := range s.CustomAnnotations {
		data[key] = value
	}

	delete(data, "CustomAnnotations")

	// Marshal the final map into JSON
	return json.Marshal(data)
}

// Schema struct contains yaml tags for reading, json for writing (creating the jsonschema)
type Schema struct {
	AdditionalProperties SchemaOrBool           `yaml:"additionalProperties,omitempty" json:"additionalProperties,omitempty"`
	Default              interface{}            `yaml:"default,omitempty"              json:"default,omitempty"`
	Then                 *Schema                `yaml:"then,omitempty"                 json:"then,omitempty"`
	PatternProperties    map[string]*Schema     `yaml:"patternProperties,omitempty"    json:"patternProperties,omitempty"`
	Properties           map[string]*Schema     `yaml:"properties,omitempty"           json:"properties,omitempty"`
	If                   *Schema                `yaml:"if,omitempty"                   json:"if,omitempty"`
	Minimum              *int                   `yaml:"minimum,omitempty"              json:"minimum,omitempty"`
	MultipleOf           *int                   `yaml:"multipleOf,omitempty"           json:"multipleOf,omitempty"`
	ExclusiveMaximum     *int                   `yaml:"exclusiveMaximum,omitempty"     json:"exclusiveMaximum,omitempty"`
	Items                *Schema                `yaml:"items,omitempty"                json:"items,omitempty"`
	ExclusiveMinimum     *int                   `yaml:"exclusiveMinimum,omitempty"     json:"exclusiveMinimum,omitempty"`
	Maximum              *int                   `yaml:"maximum,omitempty"              json:"maximum,omitempty"`
	Else                 *Schema                `yaml:"else,omitempty"                 json:"else,omitempty"`
	Pattern              string                 `yaml:"pattern,omitempty"              json:"pattern,omitempty"`
	Const                interface{}            `yaml:"const,omitempty"                json:"const,omitempty"`
	Ref                  string                 `yaml:"$ref,omitempty"                 json:"$ref,omitempty"`
	Schema               string                 `yaml:"$schema,omitempty"              json:"$schema,omitempty"`
	Id                   string                 `yaml:"$id,omitempty"                  json:"$id,omitempty"`
	Format               string                 `yaml:"format,omitempty"               json:"format,omitempty"`
	Description          string                 `yaml:"description,omitempty"          json:"description,omitempty"`
	Title                string                 `yaml:"title,omitempty"                json:"title,omitempty"`
	Type                 StringOrArrayOfString  `yaml:"type,omitempty"                 json:"type,omitempty"`
	AnyOf                []*Schema              `yaml:"anyOf,omitempty"                json:"anyOf,omitempty"`
	AllOf                []*Schema              `yaml:"allOf,omitempty"                json:"allOf,omitempty"`
	OneOf                []*Schema              `yaml:"oneOf,omitempty"                json:"oneOf,omitempty"`
	Not                  *Schema                `yaml:"not,omitempty"                json:"not,omitempty"`
	Examples             []string               `yaml:"examples,omitempty"             json:"examples,omitempty"`
	Enum                 []string               `yaml:"enum,omitempty"                 json:"enum,omitempty"`
	HasData              bool                   `yaml:"-"                              json:"-"`
	Deprecated           bool                   `yaml:"deprecated,omitempty"           json:"deprecated,omitempty"`
	ReadOnly             bool                   `yaml:"readOnly,omitempty"           json:"readOnly,omitempty"`
	WriteOnly            bool                   `yaml:"writeOnly,omitempty"           json:"writeOnly,omitempty"`
	Required             BoolOrArrayOfString    `yaml:"required,omitempty"             json:"required,omitempty"`
	CustomAnnotations    map[string]interface{} `yaml:"-"                              json:",omitempty"`
	MinLength            *int                   `yaml:"minLength,omitempty"              json:"minLength,omitempty"`
	MaxLength            *int                   `yaml:"maxLength,omitempty"              json:"maxLength,omitempty"`
	MinItems             *int                   `yaml:"minItems,omitempty"              json:"minItems,omitempty"`
	MaxItems             *int                   `yaml:"maxItems,omitempty"              json:"maxItems,omitempty"`
}

func NewSchema(schemaType string) *Schema {
	if schemaType == "" {
		return &Schema{}
	}

	return &Schema{
		Type:     []string{schemaType},
		Required: NewBoolOrArrayOfString([]string{}, false),
	}
}

func (s Schema) getJsonKeys() []string {
	result := []string{}
	t := reflect.TypeOf(s)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		result = append(result, field.Tag.Get("json"))
	}
	return result
}

// UnmarshalYAML custom unmarshal method
func (s *Schema) UnmarshalYAML(node *yaml.Node) error {
	// Create an alias type to avoid recursion
	type schemaAlias Schema
	alias := new(schemaAlias)
	// copy all existing fields
	*alias = schemaAlias(*s)

	// Unmarshal known fields into alias
	if err := node.Decode(alias); err != nil {
		return err
	}

	// Initialize CustomAnnotations map
	alias.CustomAnnotations = make(map[string]interface{})

	knownKeys := s.getJsonKeys()

	// Iterate through all node fields
	for i := 0; i < len(node.Content)-1; i += 2 {
		keyNode := node.Content[i]
		valueNode := node.Content[i+1]
		key := keyNode.Value

		if Contains(knownKeys, key) {
			continue
		}

		// Unmarshal unknown fields into the CustomAnnotations map
		if !strings.HasPrefix(key, CustomAnnotationPrefix) {
			continue
		}
		var value interface{}
		if err := valueNode.Decode(&value); err != nil {
			return err
		}
		alias.CustomAnnotations[key] = value
	}

	// Copy alias to the main struct
	*s = Schema(*alias)
	return nil
}

// Set sets the HasData field to true
func (s *Schema) Set() {
	s.HasData = true
}

// DisableRequiredProperties sets disables all required fields
func (s *Schema) DisableRequiredProperties() {
	s.Required = NewBoolOrArrayOfString([]string{}, false)
	for _, v := range s.Properties {
		v.DisableRequiredProperties()
	}
	if s.Items != nil {
		s.Items.DisableRequiredProperties()
	}

	if s.AnyOf != nil {
		for _, v := range s.AnyOf {
			v.DisableRequiredProperties()
		}
	}
	if s.OneOf != nil {
		for _, v := range s.OneOf {
			v.DisableRequiredProperties()
		}
	}
	if s.AllOf != nil {
		for _, v := range s.AllOf {
			v.DisableRequiredProperties()
		}
	}
	if s.If != nil {
		s.If.DisableRequiredProperties()
	}
	if s.Else != nil {
		s.Else.DisableRequiredProperties()
	}
	if s.Then != nil {
		s.Then.DisableRequiredProperties()
	}
	if s.Not != nil {
		s.Not.DisableRequiredProperties()
	}
}

// ToJson converts the data to raw json
func (s Schema) ToJson() ([]byte, error) {
	res, err := json.MarshalIndent(&s, "", "  ")
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Validate the schema
func (s Schema) Validate() error {
	jsonStr, err := s.ToJson()
	if err != nil {
		return err
	}

	c := jsonschema.NewCompiler()

	if err := c.AddResource("schema.json", bytes.NewReader(jsonStr)); err != nil {
		// if _, err := jsonschema.CompileString("schema.json", string(jsonStr)); err != nil {
		return err
	}

	// Check if type is valid
	if err := s.Type.Validate(); err != nil {
		return err
	}

	// Check if type=string if pattern!=""
	if s.Pattern != "" && !s.Type.IsEmpty() && !s.Type.Matches("string") {
		return fmt.Errorf("cant use pattern if type is %s. Use type=string", s.Type)
	}

	// Check if type=string if format!=""
	if s.Format != "" && !s.Type.IsEmpty() && !s.Type.Matches("string") {
		return fmt.Errorf("cant use format if type is %s. Use type=string", s.Type)
	}

	// Check if type=string if maxLength or minLength is used
	if s.MaxLength != nil && s.MinLength != nil && *s.MinLength > *s.MaxLength {
		return errors.New("cant use MinLength > MaxLength")
	}

	// Cant use Format and Pattern together
	if s.Format != "" && s.Pattern != "" {
		return errors.New("cant use format and pattern option at the same time")
	}

	// Validate nested Items schema
	if s.Items != nil {
		if err := s.Items.Validate(); err != nil {
			return err
		}
	}

	// If type and items are used, type must be array
	if s.Items != nil && !s.Type.IsEmpty() && !s.Type.Matches("array") {
		return fmt.Errorf("cant use items if type is %s. Use type=array", s.Type)
	}

	if (s.MinItems != nil || s.MaxItems != nil) && !s.Type.IsEmpty() && !s.Type.Matches("array") {
		return fmt.Errorf("cant use minItems or maxItems if type is %s. Use type=array", s.Type)
	}

	if (s.MinItems != nil && s.MaxItems != nil) && *s.MaxItems < *s.MinItems {
		return errors.New("minItems cant be greater than maxItems")
	}

	if s.Const != nil && !s.Type.IsEmpty() {
		return errors.New("if your are using const, you can't use type")
	}

	if s.Enum != nil && !s.Type.IsEmpty() {
		return errors.New("if your are using enum, you can't use type")
	}

	// Check if format is valid
	// https://json-schema.org/understanding-json-schema/reference/string.html#built-in-formats
	// We currently dont support https://datatracker.ietf.org/doc/html/rfc3339#appendix-A
	if s.Format != "" &&
		s.Format != "date-time" &&
		s.Format != "time" &&
		s.Format != "date" &&
		s.Format != "duration" &&
		s.Format != "email" &&
		s.Format != "idn-email" &&
		s.Format != "hostname" &&
		s.Format != "idn-hostname" &&
		s.Format != "ipv4" &&
		s.Format != "ipv6" &&
		s.Format != "uuid" &&
		s.Format != "uri" &&
		s.Format != "uri-reference" &&
		s.Format != "iri" &&
		s.Format != "iri-reference" &&
		s.Format != "uri-template" &&
		s.Format != "json-pointer" &&
		s.Format != "relative-json-pointer" &&
		s.Format != "regex" {
		return fmt.Errorf("the format %s is not supported", s.Format)
	}

	if s.Minimum != nil && !s.Type.IsEmpty() && !s.Type.Matches("number") && !s.Type.Matches("integer") {
		return fmt.Errorf("if you use minimum, you cant use type=%s", s.Type)
	}
	if s.Maximum != nil && !s.Type.IsEmpty() && !s.Type.Matches("number") && !s.Type.Matches("integer") {
		return fmt.Errorf("if you use maximum, you cant use type=%s", s.Type)
	}
	if s.ExclusiveMinimum != nil && !s.Type.IsEmpty() && !s.Type.Matches("number") && !s.Type.Matches("integer") {
		return fmt.Errorf("if you use exclusiveMinimum, you cant use type=%s", s.Type)
	}
	if s.ExclusiveMaximum != nil && !s.Type.IsEmpty() && !s.Type.Matches("number") && !s.Type.Matches("integer") {
		return fmt.Errorf("if you use exclusiveMaximum, you cant use type=%s", s.Type)
	}
	if s.MultipleOf != nil && !s.Type.IsEmpty() && !s.Type.Matches("number") && !s.Type.Matches("integer") {
		return fmt.Errorf("if you use multiple, you cant use type=%s", s.Type)
	}
	if s.MultipleOf != nil && *s.MultipleOf <= 0 {
		return errors.New("multiple option must be greater than 0")
	}
	if s.Minimum != nil && s.ExclusiveMinimum != nil {
		return errors.New("you cant set minimum and exclusiveMinimum")
	}
	if s.Maximum != nil && s.ExclusiveMaximum != nil {
		return errors.New("you cant set minimum and exclusiveMaximum")
	}
	return nil
}

var possibleSkipFields = []string{"title", "description", "required", "default", "additionalProperties"}

type SkipAutoGenerationConfig struct {
	Title, Description, Required, Default, AdditionalProperties bool
}

func NewSkipAutoGenerationConfig(flag []string) (*SkipAutoGenerationConfig, error) {
	var config SkipAutoGenerationConfig

	var invalidFlags []string

	for _, fieldName := range flag {
		if !Contains(possibleSkipFields, fieldName) {
			invalidFlags = append(invalidFlags, fieldName)
		}
		if fieldName == "title" {
			config.Title = true
		}
		if fieldName == "description" {
			config.Description = true
		}
		if fieldName == "required" {
			config.Required = true
		}
		if fieldName == "default" {
			config.Default = true
		}
		if fieldName == "additionalProperties" {
			config.AdditionalProperties = true
		}
	}

	if len(invalidFlags) != 0 {
		return nil, fmt.Errorf("unsupported field names '%s' for skipping auto-generation", strings.Join(invalidFlags, "', '"))
	}

	return &config, nil
}

func typeFromTag(tag string) ([]string, error) {
	switch tag {
	case nullTag:
		return []string{"null"}, nil
	case boolTag:
		return []string{"boolean"}, nil
	case strTag:
		return []string{"string"}, nil
	case intTag:
		return []string{"integer"}, nil
	case floatTag:
		return []string{"number"}, nil
	case timestampTag:
		return []string{"string"}, nil
	case arrayTag:
		return []string{"array"}, nil
	case mapTag:
		return []string{"object"}, nil
	}
	return []string{}, fmt.Errorf("unsupported yaml tag found: %s", tag)
}

// FixRequiredProperties iterates over the properties and checks if required has a boolean value.
// Then the property is added to the parents required property list
func FixRequiredProperties(schema *Schema) error {
	if schema.Properties != nil {
		for propName, propValue := range schema.Properties {
			FixRequiredProperties(propValue)
			if propValue.Required.Bool && !Contains(schema.Required.Strings, propName) {
				schema.Required.Strings = append(schema.Required.Strings, propName)
			}
		}
		if !Contains(schema.Type, "object") {
			// If .Properties is set, type must be object
			schema.Type = []string{"object"}
		}
	}

	if schema.Then != nil {
		FixRequiredProperties(schema.Then)
	}

	if schema.If != nil {
		FixRequiredProperties(schema.If)
	}

	if schema.Else != nil {
		FixRequiredProperties(schema.Else)
	}

	if schema.Items != nil {
		FixRequiredProperties(schema.Items)
	}

	if schema.AdditionalProperties != nil {
		if subSchema, ok := schema.AdditionalProperties.(Schema); ok {
			FixRequiredProperties(&subSchema)
		}
	}

	if len(schema.AnyOf) > 0 {
		for _, subSchema := range schema.AnyOf {
			FixRequiredProperties(subSchema)
		}
	}

	if len(schema.AllOf) > 0 {
		for _, subSchema := range schema.AllOf {
			FixRequiredProperties(subSchema)
		}
	}

	if len(schema.OneOf) > 0 {
		for _, subSchema := range schema.OneOf {
			FixRequiredProperties(subSchema)
		}
	}

	if schema.Not != nil {
		FixRequiredProperties(schema.Not)
	}

	return nil
}

// GetSchemaFromComment parses the annotations from the given comment
func GetSchemaFromComment(comment string) (Schema, string, error) {
	var result Schema
	scanner := bufio.NewScanner(strings.NewReader(comment))
	description := []string{}
	rawSchema := []string{}
	insideSchemaBlock := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, SchemaPrefix) {
			insideSchemaBlock = !insideSchemaBlock
			continue
		}
		if insideSchemaBlock {
			content := strings.TrimPrefix(line, CommentPrefix)
			rawSchema = append(rawSchema, strings.TrimPrefix(strings.TrimPrefix(content, CommentPrefix), " "))
			result.Set()
		} else {
			description = append(description, strings.TrimPrefix(strings.TrimPrefix(line, CommentPrefix), " "))
		}
	}

	if insideSchemaBlock {
		return result, "",
			fmt.Errorf("unclosed schema block found in comment: %s", comment)
	}

	err := yaml.Unmarshal([]byte(strings.Join(rawSchema, "\n")), &result)
	if err != nil {
		return result, "", err
	}

	return result, strings.Join(description, "\n"), nil
}

// YamlToSchema recursevly parses the given yaml.Node and creates a jsonschema from it
func YamlToSchema(
	valuesPath string,
	node *yaml.Node,
	keepFullComment bool,
	helmDocsCompatibilityMode bool,
	dontRemoveHelmDocsPrefix bool,
	skipAutoGeneration *SkipAutoGenerationConfig,
	parentRequiredProperties *[]string,
) *Schema {
	schema := NewSchema("object")
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			log.Fatalf("Strange yaml document found:\n%v\n", node.Content[:])
		}

		schema.Schema = "http://json-schema.org/draft-07/schema#"
		schema.Properties = YamlToSchema(
			valuesPath,
			node.Content[0],
			keepFullComment,
			helmDocsCompatibilityMode,
			dontRemoveHelmDocsPrefix,
			skipAutoGeneration,
			&schema.Required.Strings,
		).Properties

		// 不生成Global
		// if _, ok := schema.Properties["global"]; !ok {
		// 	// global key must be present, otherwise helm lint will fail
		// 	if schema.Properties == nil {
		// 		schema.Properties = make(map[string]*Schema)
		// 	}
		// 	schema.Properties["global"] = NewSchema(
		// 		"object",
		// 	)
		// 	if !skipAutoGeneration.Title {
		// 		schema.Properties["global"].Title = "global"
		// 	}
		// 	if !skipAutoGeneration.Description {
		// 		schema.Properties["global"].Description = "Global values are values that can be accessed from any chart or subchart by exactly the same name."
		// 	}
		// }
		// always disable on top level
		if !skipAutoGeneration.AdditionalProperties {
			schema.AdditionalProperties = new(bool)
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if valueNode.Kind == yaml.AliasNode {
				valueNode = valueNode.Alias
			}

			comment := keyNode.HeadComment
			if !keepFullComment {
				leadingCommentsRemover := regexp.MustCompile(`(?s)(?m)(?:.*\n{2,})+`)
				comment = leadingCommentsRemover.ReplaceAllString(comment, "")
			}

			keyNodeSchema, description, err := GetSchemaFromComment(comment)
			if err != nil {
				log.Fatalf("Error while parsing comment of key %s: %v", keyNode.Value, err)
			}
			if helmDocsCompatibilityMode {
				_, helmDocsValue := helm.ParseComment(strings.Split(keyNode.HeadComment, "\n"))
				if helmDocsValue.Default != "" {
					keyNodeSchema.Set()
					keyNodeSchema.Default = helmDocsValue.Default
				}
				if helmDocsValue.Description != "" {
					keyNodeSchema.Set()
					keyNodeSchema.Description = helmDocsValue.Description
				}
				if helmDocsValue.ValueType != "" {
					helmDocsType, err := helmDocsTypeToSchemaType(helmDocsValue.ValueType)
					if err != nil {
						log.Warnln(err)
					} else {
						keyNodeSchema.Set()
						keyNodeSchema.Type = StringOrArrayOfString{helmDocsType}
					}
				}
			}

			if !dontRemoveHelmDocsPrefix {
				// remove all lines containing helm-docs @tags, like @ignored, or one of those:
				// https://github.com/norwoodj/helm-docs/blob/v1.14.2/pkg/helm/chart_info.go#L18-L24
				helmDocsTagsRemover := regexp.MustCompile(`(?ms)(\r\n|\r|\n)?\s*@\w+(\s+--\s)?[^\n\r]*`)
				description = helmDocsTagsRemover.ReplaceAllString(description, "")

				prefixRemover := regexp.MustCompile(`(?m)^--\s?`)
				description = prefixRemover.ReplaceAllString(description, "")
			}

			if keyNodeSchema.Ref != "" {
				if strings.HasPrefix(keyNodeSchema.Ref, "#") {
					// 说明是一个外部依赖
					// log.Info(fmt.Sprintf("外部依赖: %s", keyNodeSchema.Ref))
				} else {
					// Check if Ref is a relative file to the values file
					refParts := strings.Split(keyNodeSchema.Ref, "#")
					if relFilePath, err := util.IsRelativeFile(valuesPath, refParts[0]); err == nil {
						var relSchema Schema
						file, err := os.Open(relFilePath)
						if err == nil {
							byteValue, _ := io.ReadAll(file)

							if len(refParts) > 1 {
								// Found json-pointer
								var obj interface{}
								json.Unmarshal(byteValue, &obj)
								jsonPointerResultRaw, err := jsonpointer.Get(obj, refParts[1])
								if err != nil {
									log.Fatal(err)
								}
								jsonPointerResultMarshaled, err := json.Marshal(jsonPointerResultRaw)
								if err != nil {
									log.Fatal(err)
								}
								err = json.Unmarshal(jsonPointerResultMarshaled, &relSchema)
								if err != nil {
									log.Fatal(err)
								}
							} else {
								// No json-pointer
								err = json.Unmarshal(byteValue, &relSchema)
								if err != nil {
									log.Fatal(err)
								}
							}
							keyNodeSchema = relSchema
							keyNodeSchema.HasData = true
						} else {
							log.Fatal(err)
						}
					} else {
						log.Debug(err)
					}
				}
			}

			if keyNodeSchema.HasData {
				if err := keyNodeSchema.Validate(); err != nil {
					log.Fatalf(
						"Error while validating jsonschema of key %s: %v",
						keyNode.Value,
						err,
					)
				}
			} else {
				nodeType, err := typeFromTag(valueNode.Tag)
				if err != nil {
					log.Fatal(err)
				}
				keyNodeSchema.Type = nodeType
			}

			// only validate or default if $ref is not set
			if keyNodeSchema.Ref == "" {

				// Add key to required array of parent
				if keyNodeSchema.Required.Bool || (len(keyNodeSchema.Required.Strings) == 0 && !skipAutoGeneration.Required && !keyNodeSchema.HasData) {
					if !Contains(*parentRequiredProperties, keyNode.Value) {
						*parentRequiredProperties = append(*parentRequiredProperties, keyNode.Value)
					}
				}

				if !skipAutoGeneration.AdditionalProperties && valueNode.Kind == yaml.MappingNode &&
					(!keyNodeSchema.HasData || keyNodeSchema.AdditionalProperties == nil) {
					keyNodeSchema.AdditionalProperties = new(bool)
				}

				// If no title was set, use the key value
				if keyNodeSchema.Title == "" && !skipAutoGeneration.Title {
					keyNodeSchema.Title = keyNode.Value
				}

				// If no description was set, use the rest of the comment as description
				if keyNodeSchema.Description == "" && !skipAutoGeneration.Description {
					keyNodeSchema.Description = description
				}

				// If no default value was set, use the values node value as default
				if !skipAutoGeneration.Default && keyNodeSchema.Default == nil && valueNode.Kind == yaml.ScalarNode {
					keyNodeSchema.Default = castNodeValueByType(valueNode.Value, keyNodeSchema.Type)
				}

				// If the value is another map and no properties are set, get them from default values
				if valueNode.Kind == yaml.MappingNode && keyNodeSchema.Properties == nil {
					keyNodeSchema.Properties = YamlToSchema(
						valuesPath,
						valueNode,
						keepFullComment,
						helmDocsCompatibilityMode,
						dontRemoveHelmDocsPrefix,
						skipAutoGeneration,
						&keyNodeSchema.Required.Strings,
					).Properties
				} else if valueNode.Kind == yaml.SequenceNode && keyNodeSchema.Items == nil {
					// If the value is a sequence, but no items are predefined
					seqSchema := NewSchema("")

					for _, itemNode := range valueNode.Content {
						if itemNode.Kind == yaml.ScalarNode {
							itemNodeType, err := typeFromTag(itemNode.Tag)
							if err != nil {
								log.Fatal(err)
							}
							seqSchema.AnyOf = append(seqSchema.AnyOf, NewSchema(itemNodeType[0]))
						} else {
							itemRequiredProperties := []string{}
							itemSchema := YamlToSchema(valuesPath, itemNode, keepFullComment, helmDocsCompatibilityMode, dontRemoveHelmDocsPrefix, skipAutoGeneration, &itemRequiredProperties)

							for _, req := range itemRequiredProperties {
								itemSchema.Required.Strings = append(itemSchema.Required.Strings, req)
							}

							if !skipAutoGeneration.AdditionalProperties && itemNode.Kind == yaml.MappingNode && (!itemSchema.HasData || itemSchema.AdditionalProperties == nil) {
								itemSchema.AdditionalProperties = new(bool)
							}

							seqSchema.AnyOf = append(seqSchema.AnyOf, itemSchema)
						}
					}
					keyNodeSchema.Items = seqSchema

					// Because the `required` field isn't valid jsonschema (but just a helper boolean)
					// we must convert them to valid requiredProperties fields
					FixRequiredProperties(&keyNodeSchema)
				}
			}

			if schema.Properties == nil {
				schema.Properties = make(map[string]*Schema)
			}
			schema.Properties[keyNode.Value] = &keyNodeSchema
		}
	}

	return schema
}

func helmDocsTypeToSchemaType(helmDocsType string) (string, error) {
	switch helmDocsType {
	case "int":
		return "integer", nil
	case "bool":
		return "boolean", nil
	case "float":
		return "number", nil
	case "list":
		return "array", nil
	case "map":
		return "object", nil
	case "string", "object":
		return helmDocsType, nil
	}

	return "", fmt.Errorf("cant translate helm-docs type (%s) to helm-schema type", helmDocsType)
}

func castNodeValueByType(rawValue string, fieldType StringOrArrayOfString) any {
	if len(fieldType) == 0 {
		return rawValue
	}

	// rawValue must be one of fielTypes
	for _, t := range fieldType {
		switch t {
		case "boolean":
			switch rawValue {
			case "true":
				return true
			case "false":
				return false
			}
		case "integer":
			v, err := strconv.Atoi(rawValue)
			if err == nil {
				return v
			}
		case "number":
			v, err := strconv.ParseFloat(rawValue, 64)
			if err == nil {
				return v
			}
		}
	}

	return rawValue
}

// Contains reports whether v is present in s.
func Contains[S ~[]E, E comparable](s S, v E) bool {
	return Index(s, v) >= 0
}

// Index returns the index of the first occurrence of v in s,
// or -1 if not present.
func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if v == s[i] {
			return i
		}
	}
	return -1
}
