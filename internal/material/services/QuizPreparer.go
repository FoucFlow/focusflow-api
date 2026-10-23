// Package services contains the business-logic layer for the Course Material Service.
package services

// QuizPreparer is the minimal contract that CourseMaterialService needs in
// order to kick off background question generation when new lessons are
// added. We keep this as a tiny interface (rather than importing the full
// *QuizService) so the wiring stays one-directional: services depending on
// services rather than tangled cycles.
//
// QuizService implements this trivially via its existing Prepare method.
// When Gemini is disabled at boot, main.go wires a nil QuizPreparer into
// CourseMaterialService, which makes pre-generation a no-op.
type QuizPreparer interface {
	// Prepare triggers idempotent background generation for the given
	// lesson. Returns immediately whether generation runs, is already in
	// flight, or finds cached questions — the caller doesn't care.
	Prepare(videoID int) (PrepareStatus, error)
}

// TranscriptWarmer is the minimal contract CourseMaterialService needs in
// order to pre-extract lesson transcripts at bulk-insert time. By saving
// the transcript when the lesson is first added, every subsequent
// Feynman reflection submission for that lesson lands on the cheap
// text-only analysis path — no second video-ingestion Gemini call.
//
// TranscriptService implements this trivially via its existing WarmAsync
// method. When Gemini is disabled at boot, main.go passes nil and the
// warm becomes a no-op.
type TranscriptWarmer interface {
	// WarmAsync fires a background goroutine that extracts and caches
	// the transcript for the given lesson. Idempotent — repeated calls
	// for the same lesson are coalesced inside the implementation.
	WarmAsync(videoID int)
}
