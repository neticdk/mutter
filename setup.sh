#!/usr/bin/env bash
# One-time GCP setup for mutter. Run as a project owner.
set -euo pipefail

if [[ $# -ne 2 ]]; then
	echo "usage: $0 PROJECT_ID USERS_GROUP_EMAIL" >&2
	exit 1
fi
# Accept a project number too, since some gcloud commands reject numbers.
project=$(gcloud projects describe "$1" --format 'value(projectId)')
group=$2
topic=mutter-events

gcloud services enable --project "$project" \
	chat.googleapis.com \
	workspaceevents.googleapis.com \
	pubsub.googleapis.com

gcloud pubsub topics describe "$topic" --project "$project" >/dev/null 2>&1 ||
	gcloud pubsub topics create "$topic" --project "$project"

gcloud pubsub topics add-iam-policy-binding "$topic" --project "$project" \
	--member serviceAccount:chat-api-push@system.gserviceaccount.com \
	--role roles/pubsub.publisher >/dev/null

# Each client creates its own filtered subscription on the shared topic.
gcloud projects add-iam-policy-binding "$project" \
	--member "group:$group" \
	--role roles/pubsub.editor --condition None >/dev/null

cat <<EOF

gcloud steps done. Three steps have no CLI or API, so do them in the console:

1. Chat app configuration (app name, avatar URL, description):
   https://console.cloud.google.com/apis/api/chat.googleapis.com/hangouts-chat?project=$project
2. OAuth branding, with audience set to Internal:
   https://console.cloud.google.com/auth/branding?project=$project
3. OAuth client of type Desktop app:
   https://console.cloud.google.com/auth/clients/create?project=$project

Then build mutter with the client baked in, so users need no setup:

   go build -ldflags "-X main.clientID=<ID> -X main.clientSecret=<SECRET>" .
EOF
