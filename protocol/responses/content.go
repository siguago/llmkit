package responses

import (
	"encoding/json"
	"fmt"
)

const (
	ContentTypeInputText     = "input_text"
	ContentTypeOutputText    = "output_text"
	ContentTypeRefusal       = "refusal"
	ContentTypeInputImage    = "input_image"
	ContentTypeInputFile     = "input_file"
	ContentTypeSummaryText   = "summary_text"
	ContentTypeReasoningText = "reasoning_text"
)

// ContentPart is a tagged content union. Known variants use their typed member
// and retain future object members in that member's ExtraFields. Raw is used
// only for an unknown content type and is round-tripped byte-for-byte.
type ContentPart struct {
	Type          string          `json:"-"`
	InputText     *InputText      `json:"-"`
	OutputText    *OutputText     `json:"-"`
	Refusal       *Refusal        `json:"-"`
	InputImage    *InputImage     `json:"-"`
	InputFile     *InputFile      `json:"-"`
	SummaryText   *SummaryText    `json:"-"`
	ReasoningText *ReasoningText  `json:"-"`
	Raw           json.RawMessage `json:"-"`
}

type InputText struct {
	Type        string      `json:"-"`
	Text        string      `json:"text"`
	ExtraFields ExtraFields `json:"-"`
}

type OutputText struct {
	Type        string            `json:"-"`
	Text        string            `json:"text"`
	Annotations []Annotation      `json:"annotations"`
	Logprobs    []json.RawMessage `json:"logprobs,omitempty"`
	ExtraFields ExtraFields       `json:"-"`
}

type Refusal struct {
	Type        string      `json:"-"`
	Refusal     string      `json:"refusal"`
	ExtraFields ExtraFields `json:"-"`
}

type InputImage struct {
	Type        string      `json:"-"`
	Detail      string      `json:"detail,omitempty"`
	FileID      string      `json:"file_id,omitempty"`
	ImageURL    string      `json:"image_url,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

type InputFile struct {
	Type        string      `json:"-"`
	FileData    string      `json:"file_data,omitempty"`
	FileID      string      `json:"file_id,omitempty"`
	FileURL     string      `json:"file_url,omitempty"`
	Filename    string      `json:"filename,omitempty"`
	Detail      string      `json:"detail,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

type SummaryText struct {
	Type        string      `json:"-"`
	Text        string      `json:"text"`
	ExtraFields ExtraFields `json:"-"`
}

type ReasoningText struct {
	Type        string      `json:"-"`
	Text        string      `json:"text"`
	ExtraFields ExtraFields `json:"-"`
}

// Annotation models fields shared by current citation variants and retains all
// future fields. Type remains open because annotation variants evolve
// independently of the surrounding output_text part.
type Annotation struct {
	Type        string      `json:"type"`
	FileID      string      `json:"file_id,omitempty"`
	Filename    string      `json:"filename,omitempty"`
	ContainerID string      `json:"container_id,omitempty"`
	Index       *int        `json:"index,omitempty"`
	URL         string      `json:"url,omitempty"`
	Title       string      `json:"title,omitempty"`
	StartIndex  *int        `json:"start_index,omitempty"`
	EndIndex    *int        `json:"end_index,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

var (
	inputTextFields     = reservedFields("type", "text")
	outputTextFields    = reservedFields("type", "text", "annotations", "logprobs")
	refusalFields       = reservedFields("type", "refusal")
	inputImageFields    = reservedFields("type", "detail", "file_id", "image_url")
	inputFileFields     = reservedFields("type", "file_data", "file_id", "file_url", "filename", "detail")
	summaryTextFields   = reservedFields("type", "text")
	reasoningTextFields = reservedFields("type", "text")
	annotationFields    = reservedFields("type", "file_id", "filename", "container_id", "index", "url", "title", "start_index", "end_index")
)

func NewInputTextPart(text string) ContentPart {
	v := &InputText{Type: ContentTypeInputText, Text: text}
	return ContentPart{Type: ContentTypeInputText, InputText: v}
}

func NewOutputTextPart(text string) ContentPart {
	v := &OutputText{Type: ContentTypeOutputText, Text: text}
	return ContentPart{Type: ContentTypeOutputText, OutputText: v}
}

func NewRefusalPart(refusal string) ContentPart {
	v := &Refusal{Type: ContentTypeRefusal, Refusal: refusal}
	return ContentPart{Type: ContentTypeRefusal, Refusal: v}
}

func NewInputImagePart(imageURL string) ContentPart {
	v := &InputImage{Type: ContentTypeInputImage, ImageURL: imageURL}
	return ContentPart{Type: ContentTypeInputImage, InputImage: v}
}

func NewInputFilePart(fileID string) ContentPart {
	v := &InputFile{Type: ContentTypeInputFile, FileID: fileID}
	return ContentPart{Type: ContentTypeInputFile, InputFile: v}
}

func NewSummaryTextPart(text string) ContentPart {
	v := &SummaryText{Type: ContentTypeSummaryText, Text: text}
	return ContentPart{Type: ContentTypeSummaryText, SummaryText: v}
}

func NewReasoningTextPart(text string) ContentPart {
	v := &ReasoningText{Type: ContentTypeReasoningText, Text: text}
	return ContentPart{Type: ContentTypeReasoningText, ReasoningText: v}
}

func NewRawContentPart(raw json.RawMessage) (ContentPart, error) {
	var part ContentPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return ContentPart{}, err
	}
	return part, nil
}

func (p ContentPart) MarshalJSON() ([]byte, error) {
	count := variantCount(
		p.InputText != nil, p.OutputText != nil, p.Refusal != nil,
		p.InputImage != nil, p.InputFile != nil, p.SummaryText != nil,
		p.ReasoningText != nil, len(p.Raw) > 0,
	)
	if count != 1 {
		return nil, fmt.Errorf("%w: ContentPart has %d variants", ErrInvalidUnion, count)
	}
	if len(p.Raw) > 0 {
		if err := checkUnknownDiscriminator(p.Raw, p.Type); err != nil {
			return nil, err
		}
		if isKnownContentType(p.Type) {
			return nil, fmt.Errorf("%w: known content type %q cannot use Raw", ErrInvalidUnion, p.Type)
		}
		return cloneRaw(p.Raw), nil
	}

	var canonical string
	var value any
	switch {
	case p.InputText != nil:
		canonical, value = ContentTypeInputText, p.InputText
	case p.OutputText != nil:
		canonical, value = ContentTypeOutputText, p.OutputText
	case p.Refusal != nil:
		canonical, value = ContentTypeRefusal, p.Refusal
	case p.InputImage != nil:
		canonical, value = ContentTypeInputImage, p.InputImage
	case p.InputFile != nil:
		canonical, value = ContentTypeInputFile, p.InputFile
	case p.SummaryText != nil:
		canonical, value = ContentTypeSummaryText, p.SummaryText
	default:
		canonical, value = ContentTypeReasoningText, p.ReasoningText
	}
	if err := checkDiscriminator(p.Type, canonical, "content"); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (p *ContentPart) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("responses: cannot unmarshal ContentPart into nil receiver")
	}
	if err := requireJSONObject(data, "content part"); err != nil {
		return err
	}
	typ, err := discriminator(data)
	if err != nil {
		return err
	}
	*p = ContentPart{Type: typ}
	switch typ {
	case ContentTypeInputText:
		p.InputText = new(InputText)
		err = json.Unmarshal(data, p.InputText)
	case ContentTypeOutputText:
		p.OutputText = new(OutputText)
		err = json.Unmarshal(data, p.OutputText)
	case ContentTypeRefusal:
		p.Refusal = new(Refusal)
		err = json.Unmarshal(data, p.Refusal)
	case ContentTypeInputImage:
		p.InputImage = new(InputImage)
		err = json.Unmarshal(data, p.InputImage)
	case ContentTypeInputFile:
		p.InputFile = new(InputFile)
		err = json.Unmarshal(data, p.InputFile)
	case ContentTypeSummaryText:
		p.SummaryText = new(SummaryText)
		err = json.Unmarshal(data, p.SummaryText)
	case ContentTypeReasoningText:
		p.ReasoningText = new(ReasoningText)
		err = json.Unmarshal(data, p.ReasoningText)
	default:
		p.Raw = cloneRaw(data)
	}
	return err
}

// RawJSON returns an owned copy for an unknown content variant. Known variants
// return nil because their typed fields and ExtraFields are authoritative.
func (p ContentPart) RawJSON() json.RawMessage { return cloneRaw(p.Raw) }

func isKnownContentType(typ string) bool {
	switch typ {
	case ContentTypeInputText, ContentTypeOutputText, ContentTypeRefusal,
		ContentTypeInputImage, ContentTypeInputFile, ContentTypeSummaryText,
		ContentTypeReasoningText:
		return true
	default:
		return false
	}
}

func (v InputText) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeInputText, "InputText"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		Text string `json:"text"`
	}{v.Text}, v.ExtraFields, ContentTypeInputText, inputTextFields)
}

func (v *InputText) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeInputText, "InputText"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, inputTextFields)
	if err != nil {
		return err
	}
	*v = InputText{Type: wire.Type, Text: wire.Text, ExtraFields: extra}
	return nil
}

func (v OutputText) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeOutputText, "OutputText"); err != nil {
		return nil, err
	}
	annotations := v.Annotations
	if annotations == nil {
		annotations = []Annotation{}
	}
	var logprobs *[]json.RawMessage
	if v.Logprobs != nil {
		logprobs = &v.Logprobs
	}
	return marshalDiscriminatedObject(struct {
		Text        string             `json:"text"`
		Annotations []Annotation       `json:"annotations"`
		Logprobs    *[]json.RawMessage `json:"logprobs,omitempty"`
	}{v.Text, annotations, logprobs}, v.ExtraFields, ContentTypeOutputText, outputTextFields)
}

func (v *OutputText) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type        string            `json:"type"`
		Text        string            `json:"text"`
		Annotations []Annotation      `json:"annotations,omitempty"`
		Logprobs    []json.RawMessage `json:"logprobs,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeOutputText, "OutputText"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, outputTextFields)
	if err != nil {
		return err
	}
	*v = OutputText{Type: wire.Type, Text: wire.Text, Annotations: wire.Annotations, Logprobs: wire.Logprobs, ExtraFields: extra}
	return nil
}

func (v Refusal) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeRefusal, "Refusal"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		Refusal string `json:"refusal"`
	}{v.Refusal}, v.ExtraFields, ContentTypeRefusal, refusalFields)
}

func (v *Refusal) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type    string `json:"type"`
		Refusal string `json:"refusal"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeRefusal, "Refusal"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, refusalFields)
	if err != nil {
		return err
	}
	*v = Refusal{Type: wire.Type, Refusal: wire.Refusal, ExtraFields: extra}
	return nil
}

func (v InputImage) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeInputImage, "InputImage"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		Detail   string `json:"detail,omitempty"`
		FileID   string `json:"file_id,omitempty"`
		ImageURL string `json:"image_url,omitempty"`
	}{v.Detail, v.FileID, v.ImageURL}, v.ExtraFields, ContentTypeInputImage, inputImageFields)
}

func (v *InputImage) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type     string `json:"type"`
		Detail   string `json:"detail,omitempty"`
		FileID   string `json:"file_id,omitempty"`
		ImageURL string `json:"image_url,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeInputImage, "InputImage"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, inputImageFields)
	if err != nil {
		return err
	}
	*v = InputImage{Type: wire.Type, Detail: wire.Detail, FileID: wire.FileID, ImageURL: wire.ImageURL, ExtraFields: extra}
	return nil
}

func (v InputFile) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeInputFile, "InputFile"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		FileData string `json:"file_data,omitempty"`
		FileID   string `json:"file_id,omitempty"`
		FileURL  string `json:"file_url,omitempty"`
		Filename string `json:"filename,omitempty"`
		Detail   string `json:"detail,omitempty"`
	}{v.FileData, v.FileID, v.FileURL, v.Filename, v.Detail}, v.ExtraFields, ContentTypeInputFile, inputFileFields)
}

func (v *InputFile) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type     string `json:"type"`
		FileData string `json:"file_data,omitempty"`
		FileID   string `json:"file_id,omitempty"`
		FileURL  string `json:"file_url,omitempty"`
		Filename string `json:"filename,omitempty"`
		Detail   string `json:"detail,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeInputFile, "InputFile"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, inputFileFields)
	if err != nil {
		return err
	}
	*v = InputFile{Type: wire.Type, FileData: wire.FileData, FileID: wire.FileID, FileURL: wire.FileURL, Filename: wire.Filename, Detail: wire.Detail, ExtraFields: extra}
	return nil
}

func (v SummaryText) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeSummaryText, "SummaryText"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		Text string `json:"text"`
	}{v.Text}, v.ExtraFields, ContentTypeSummaryText, summaryTextFields)
}

func (v *SummaryText) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeSummaryText, "SummaryText"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, summaryTextFields)
	if err != nil {
		return err
	}
	*v = SummaryText{Type: wire.Type, Text: wire.Text, ExtraFields: extra}
	return nil
}

func (v ReasoningText) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ContentTypeReasoningText, "ReasoningText"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		Text string `json:"text"`
	}{v.Text}, v.ExtraFields, ContentTypeReasoningText, reasoningTextFields)
}

func (v *ReasoningText) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ContentTypeReasoningText, "ReasoningText"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, reasoningTextFields)
	if err != nil {
		return err
	}
	*v = ReasoningText{Type: wire.Type, Text: wire.Text, ExtraFields: extra}
	return nil
}

func (a Annotation) MarshalJSON() ([]byte, error) {
	type wire struct {
		Type        string `json:"type"`
		FileID      string `json:"file_id,omitempty"`
		Filename    string `json:"filename,omitempty"`
		ContainerID string `json:"container_id,omitempty"`
		Index       *int   `json:"index,omitempty"`
		URL         string `json:"url,omitempty"`
		Title       string `json:"title,omitempty"`
		StartIndex  *int   `json:"start_index,omitempty"`
		EndIndex    *int   `json:"end_index,omitempty"`
	}
	return marshalObjectWithExtra(wire{
		a.Type, a.FileID, a.Filename, a.ContainerID, a.Index,
		a.URL, a.Title, a.StartIndex, a.EndIndex,
	}, a.ExtraFields, annotationFields)
}

func (a *Annotation) UnmarshalJSON(data []byte) error {
	type wire struct {
		Type        string `json:"type"`
		FileID      string `json:"file_id,omitempty"`
		Filename    string `json:"filename,omitempty"`
		ContainerID string `json:"container_id,omitempty"`
		Index       *int   `json:"index,omitempty"`
		URL         string `json:"url,omitempty"`
		Title       string `json:"title,omitempty"`
		StartIndex  *int   `json:"start_index,omitempty"`
		EndIndex    *int   `json:"end_index,omitempty"`
	}
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, annotationFields)
	if err != nil {
		return err
	}
	*a = Annotation{
		Type: decoded.Type, FileID: decoded.FileID, Filename: decoded.Filename,
		ContainerID: decoded.ContainerID, Index: decoded.Index, URL: decoded.URL,
		Title: decoded.Title, StartIndex: decoded.StartIndex, EndIndex: decoded.EndIndex,
		ExtraFields: extra,
	}
	return nil
}
