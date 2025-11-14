#!/bin/bash
set -euo pipefail

# Get current branch
BRANCH=$(git branch --show-current)

# Find PR for current branch
PR_NUMBER=$(gh pr list --head "$BRANCH" --json number --jq '.[0].number')

# Get latest workflow run for the branch
RUN_ID=$(gh run list --branch "$BRANCH" --limit 1 --json databaseId --jq '.[0].databaseId')

# Wait for workflow to complete
while true; do
  STATUS=$(gh run view "$RUN_ID" --json status --jq '.status')
  if [ "$STATUS" = "completed" ]; then
    break
  fi
  sleep 10
done

# Get all jobs with step details
JOBS_JSON=$(gh run view "$RUN_ID" --json jobs)

# Create logs directory
mkdir -p "logs/$RUN_ID"

# Process each job
echo "$JOBS_JSON" | jq -c '.jobs[]' | while read -r job; do
  JOB_ID=$(echo "$job" | jq -r '.databaseId')
  JOB_NAME=$(echo "$job" | jq -r '.name')
  JOB_CONCLUSION=$(echo "$job" | jq -r '.conclusion')
  
  # Sanitize job name for filename
  JOB_FILE=$(echo "$JOB_NAME" | tr '[:upper:]' '[:lower:]' | tr ' ' '-' | tr -cd '[:alnum:]-')
  
  # Print job header
  echo ""
  echo "=== Job: $JOB_NAME (${JOB_CONCLUSION^^}) ==="
  
  # Download logs if job failed
  if [ "$JOB_CONCLUSION" = "failure" ]; then
    # Download full log and strip timestamps/prefixes
    FULL_LOG="logs/$RUN_ID/$JOB_FILE.log"
    gh run view --job "$JOB_ID" --log | sed -E 's/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]+Z //g' | sed 's/^##\[.*\]//g' > "$FULL_LOG"
    FULL_SIZE=$(du -h "$FULL_LOG" | cut -f1)
    
    # Download failed steps log and strip timestamps/prefixes
    FAILED_LOG="logs/$RUN_ID/$JOB_FILE-failed.log"
    gh run view --job "$JOB_ID" --log-failed | sed -E 's/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]+Z //g' | sed 's/^##\[.*\]//g' > "$FAILED_LOG"
    FAILED_SIZE=$(du -h "$FAILED_LOG" | cut -f1)
    
    echo "• Full log: $FULL_LOG ($FULL_SIZE)"
    echo "• Failed steps only: $FAILED_LOG ($FAILED_SIZE)"
  else
    echo "• No logs (job ${JOB_CONCLUSION})"
  fi
  
  # Print steps as bullet points
  echo ""
  echo "Steps:"
  echo "$job" | jq -c '.steps[]' | while read -r step; do
    STEP_NAME=$(echo "$step" | jq -r '.name')
    STEP_CONCLUSION=$(echo "$step" | jq -r '.conclusion')
    STEP_NUMBER=$(echo "$step" | jq -r '.number')
    
    # Format status indicator
    case "$STEP_CONCLUSION" in
      success) INDICATOR="✓" ;;
      failure) INDICATOR="✗" ;;
      skipped) INDICATOR="○" ;;
      cancelled) INDICATOR="⊘" ;;
      *) INDICATOR="?" ;;
    esac
    
    echo "  $INDICATOR [$STEP_NUMBER] $STEP_NAME"
  done
done