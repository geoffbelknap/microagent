package main

// progressBehavior classifies command intent independently from the terminal
// renderer. Interactive human progress may animate; these values describe
// whether the underlying command is determinate, delayed, readiness-only,
// streaming after connection, or intentionally quiet.
type progressBehavior string

const (
	progressBehaviorAnimated    progressBehavior = "animated"
	progressBehaviorDeterminate progressBehavior = "determinate"
	progressBehaviorDelayed     progressBehavior = "delayed"
	progressBehaviorReadiness   progressBehavior = "readiness-only"
	progressBehaviorStreaming   progressBehavior = "streaming"
	progressBehaviorNone        progressBehavior = "no-progress"
)

// progressCommandInventory is the code-owned audit of every public top-level
// command. Commands with resource subverbs may carry more than one behavior.
// Tests require every registry entry to appear here and keep no-progress
// exclusive, so additions require an intentional presentation decision.
var progressCommandInventory = map[string][]progressBehavior{
	"init":       {progressBehaviorNone},
	"doctor":     {progressBehaviorAnimated, progressBehaviorDelayed},
	"run":        {progressBehaviorAnimated, progressBehaviorDeterminate},
	"dispatch":   {progressBehaviorAnimated, progressBehaviorDeterminate},
	"create":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"start":      {progressBehaviorAnimated, progressBehaviorDeterminate, progressBehaviorDelayed},
	"exec":       {progressBehaviorAnimated, progressBehaviorDelayed, progressBehaviorStreaming},
	"connect":    {progressBehaviorAnimated, progressBehaviorStreaming},
	"apply":      {progressBehaviorAnimated, progressBehaviorDelayed},
	"supervise":  {progressBehaviorAnimated, progressBehaviorReadiness},
	"status":     {progressBehaviorNone},
	"wait":       {progressBehaviorAnimated, progressBehaviorDelayed},
	"halt":       {progressBehaviorAnimated, progressBehaviorDeterminate},
	"kill":       {progressBehaviorAnimated, progressBehaviorDelayed},
	"pause":      {progressBehaviorAnimated, progressBehaviorDelayed},
	"resume":     {progressBehaviorAnimated, progressBehaviorDelayed},
	"quarantine": {progressBehaviorAnimated, progressBehaviorDeterminate},
	"delete":     {progressBehaviorAnimated, progressBehaviorDelayed, progressBehaviorDeterminate},
	"list":       {progressBehaviorNone},
	"ps":         {progressBehaviorNone},
	"logs":       {progressBehaviorAnimated, progressBehaviorStreaming},
	"events":     {progressBehaviorAnimated, progressBehaviorStreaming},
	"egress":     {progressBehaviorAnimated, progressBehaviorStreaming},
	"stats":      {progressBehaviorAnimated, progressBehaviorStreaming},
	"result":     {progressBehaviorNone},
	"cp":         {progressBehaviorAnimated, progressBehaviorDeterminate},
	"artifact":   {progressBehaviorAnimated, progressBehaviorDeterminate},
	"snapshot":   {progressBehaviorAnimated, progressBehaviorDeterminate},
	"clone":      {progressBehaviorAnimated, progressBehaviorDeterminate},
	"commit":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"resize":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"image":      {progressBehaviorAnimated, progressBehaviorDeterminate, progressBehaviorDelayed},
	"volume":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"network":    {progressBehaviorNone},
	"model":      {progressBehaviorAnimated, progressBehaviorDeterminate, progressBehaviorDelayed},
	"secret":     {progressBehaviorNone},
	"registry":   {progressBehaviorNone},
	"rootfs":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"kernel":     {progressBehaviorAnimated, progressBehaviorDeterminate},
	"profiles":   {progressBehaviorNone},
	"serve":      {progressBehaviorAnimated, progressBehaviorReadiness},
	"host":       {progressBehaviorNone},
	"contract":   {progressBehaviorNone},
	"perf":       {progressBehaviorAnimated, progressBehaviorDeterminate},
	"gc":         {progressBehaviorAnimated, progressBehaviorDeterminate},
}
