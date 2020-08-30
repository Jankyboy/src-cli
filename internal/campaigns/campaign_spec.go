package campaigns

import (
	"encoding/json"
	"fmt"

	"github.com/gobwas/glob"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/campaigns/graphql"
	"github.com/sourcegraph/src-cli/schema"
	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v2"
)

// Some general notes about the struct definitions below.
//
// 1. They map _very_ closely to the campaign spec JSON schema. We don't
//    auto-generate the types because we need YAML support (more on that in a
//    moment) and because no generator can currently handle oneOf fields
//    gracefully in Go, but that's a potential future enhancement.
//
// 2. Fields are tagged with _both_ JSON and YAML tags. Internally, the JSON
//    schema library needs to be able to marshal the struct to JSON for
//    validation, so we need to ensure that we're generating the right JSON to
//    represent the YAML that we unmarshalled.
//
// 3. All JSON tags include omitempty so that the schema validation can pick up
//    omitted fields. The other option here was to have everything unmarshal to
//    pointers, which is ugly and inefficient.

type CampaignSpec struct {
	Name              string                `json:"name,omitempty" yaml:"name"`
	Description       string                `json:"description,omitempty" yaml:"description"`
	On                []OnQueryOrRepository `json:"on,omitempty" yaml:"on"`
	Steps             []Step                `json:"steps,omitempty" yaml:"steps"`
	ImportChangesets  []ImportChangeset     `json:"importChangesets,omitempty" yaml:"importChangesets"`
	ChangesetTemplate *ChangesetTemplate    `json:"changesetTemplate,omitempty" yaml:"changesetTemplate"`
}

type ChangesetTemplate struct {
	Title     OverridableString            `json:"title,omitempty" yaml:"title"`
	Body      string                       `json:"body,omitempty" yaml:"body"`
	Branch    string                       `json:"branch,omitempty" yaml:"branch"`
	Commit    ExpandedGitCommitDescription `json:"commit,omitempty" yaml:"commit"`
	Published OverridableBool              `json:"published" yaml:"published"`
}

type ExpandedGitCommitDescription struct {
	Message string `json:"message,omitempty" yaml:"message"`
}

type ImportChangeset struct {
	Repository  string        `json:"repository" yaml:"repository"`
	ExternalIDs []interface{} `json:"externalIDs" yaml:"externalIDs"`
}

type OnQueryOrRepository struct {
	RepositoriesMatchingQuery string `json:"repositoriesMatchingQuery,omitempty" yaml:"repositoriesMatchingQuery"`
	Repository                string `json:"repository,omitempty" yaml:"repository"`
	Branch                    string `json:"branch,omitempty" yaml:"branch"`
}

type OverridableBool struct {
	Default *bool
	OnlyExcept
}

type OnlyExcept struct {
	Only   []string `json:"only,omitempty" yaml:"only"`
	Except []string `json:"except,omitempty" yaml:"except"`

	only   []glob.Glob
	except []glob.Glob
}

func (p *OverridableBool) IsRepoPublished(repo *graphql.Repository) bool {
	if p.Default != nil {
		return *p.Default
	}

	if len(p.only) > 0 {
		for _, g := range p.only {
			if g.Match(repo.Name) {
				return true
			}
		}
		return false
	}

	for _, g := range p.except {
		if g.Match(repo.Name) {
			return false
		}
	}
	return true
}

func (p *OverridableBool) MarshalJSON() ([]byte, error) {
	if p.Default != nil {
		return json.Marshal(*p.Default)
	}

	return json.Marshal(&p.OnlyExcept)
}

func (p *OverridableBool) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var def bool
	if err := unmarshal(&def); err == nil {
		p.Default = &def
		return nil
	}

	p.Default = nil
	if err := unmarshal(&p.OnlyExcept); err != nil {
		return err
	}

	var err error
	p.only, err = compilePatterns(p.Only)
	if err != nil {
		return err
	}

	p.except, err = compilePatterns(p.Except)
	if err != nil {
		return err
	}

	return nil
}

func compilePatterns(patterns []string) ([]glob.Glob, error) {
	globs := make([]glob.Glob, len(patterns))
	for i, pattern := range patterns {
		g, err := glob.Compile(pattern)
		if err != nil {
			return nil, errors.Wrapf(err, "compiling repo pattern %q", pattern)
		}

		globs[i] = g
	}

	return globs, nil
}

type OverridableString struct {
	Default string
	Only    []*MatchValue
}

type MatchValue struct {
	Match string `json:"match,omitempty" yaml:"match"`
	Value string `json:"value,omitempty" yaml:"value"`

	match glob.Glob
}

func (o *OverridableString) Value(repo *graphql.Repository) string {
	for _, mv := range o.Only {
		if mv.match.Match(repo.Name) {
			return mv.Value
		}
	}

	return o.Default
}

func (o *OverridableString) MarshalJSON() ([]byte, error) {
	if len(o.Only) == 0 {
		return json.Marshal(o.Default)
	}

	return json.Marshal(&struct {
		Default string        `json:"default,omitempty"`
		Only    []*MatchValue `json:"only,omitempty"`
	}{
		Default: o.Default,
		Only:    o.Only,
	})
}

func (o *OverridableString) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		o.Default = s
		o.Only = []*MatchValue{}
		return nil
	}

	var temp struct {
		Default string        `yaml:"default"`
		Only    []*MatchValue `yaml:"only"`
	}
	if err := unmarshal(&temp); err != nil {
		return err
	}

	o.Default = temp.Default
	o.Only = temp.Only
	for _, mv := range o.Only {
		var err error
		if mv.match, err = glob.Compile(mv.Match); err != nil {
			return errors.Wrapf(err, "compiling repo pattern %q", mv.match)
		}
	}

	return nil
}

type Step struct {
	Run       string            `json:"run,omitempty" yaml:"run"`
	Container string            `json:"container,omitempty" yaml:"container"`
	Env       map[string]string `json:"env,omitempty" yaml:"env"`

	image string
}

func ParseCampaignSpec(data []byte) (*CampaignSpec, error) {
	var spec CampaignSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}

	return &spec, nil
}

var campaignSpecSchema *gojsonschema.Schema

func (spec *CampaignSpec) Validate() error {
	if campaignSpecSchema == nil {
		var err error
		campaignSpecSchema, err = gojsonschema.NewSchemaLoader().Compile(gojsonschema.NewStringLoader(schema.CampaignSpecJSON))
		if err != nil {
			return errors.Wrap(err, "parsing campaign spec schema")
		}
	}

	result, err := campaignSpecSchema.Validate(gojsonschema.NewGoLoader(spec))
	if err != nil {
		return errors.Wrapf(err, "validating campaign spec")
	}
	if result.Valid() {
		return nil
	}

	var errs *multierror.Error
	for _, verr := range result.Errors() {
		// ResultError instances don't actually implement error, so we need to
		// wrap them as best we can before adding them to the multierror.
		errs = multierror.Append(errs, errors.New(verr.String()))
	}
	return errs
}

func (on *OnQueryOrRepository) String() string {
	if on.RepositoriesMatchingQuery != "" {
		return on.RepositoriesMatchingQuery
	} else if on.Repository != "" {
		return "r:" + on.Repository
	}

	return fmt.Sprintf("%v", *on)
}
