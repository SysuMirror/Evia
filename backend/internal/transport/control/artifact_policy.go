package control

// Artifact language marker, mirrored by the single-value
// TypeScriptArtifact.language enum and guarded by
// TestArtifactLanguageVocabularyContract. The artifacts package owns the
// emitted source contents; this constant only names the wire vocabulary.
const artifactLanguageTypeScript = "typescript"

// artifactLanguages is the single Go-side source of truth for the published
// artifact languages.
var artifactLanguages = []string{artifactLanguageTypeScript}
