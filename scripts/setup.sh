#!/usr/bin/env bash
# One-time GCP setup for mutter. Run as a project owner.
set -euo pipefail

# --per-user sets up per-user topics, which the provisioning script in
# docs/organizations.md creates. Without it, all users share one topic.
per_user=false
if [[ ${1:-} == --per-user ]]; then
	per_user=true
	shift
fi
if [[ $# -ne 2 ]]; then
	echo "usage: $0 [--per-user] PROJECT_ID USERS_GROUP_EMAIL" >&2
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

if [[ $per_user == false ]]; then
	gcloud pubsub topics describe "$topic" --project "$project" >/dev/null 2>&1 ||
		gcloud pubsub topics create "$topic" --project "$project"

	gcloud pubsub topics add-iam-policy-binding "$topic" --project "$project" \
		--member serviceAccount:chat-api-push@system.gserviceaccount.com \
		--role roles/pubsub.publisher >/dev/null

	# Each client creates its own filtered subscription on the shared topic.
	gcloud projects add-iam-policy-binding "$project" \
		--member "group:$group" \
		--role roles/pubsub.editor --condition None >/dev/null
fi

if [[ $per_user == true ]]; then
	live="\"topic_project\": \"$project\""
else
	live="\"topic\": \"projects/$project/topics/$topic\""
fi

cat <<EOF

gcloud steps done. Three steps have no CLI or API, so do them in the console:

1. Chat app configuration (app name, avatar URL, description):
   https://console.cloud.google.com/apis/api/chat.googleapis.com/hangouts-chat?project=$project
2. OAuth branding, with audience set to Internal:
   https://console.cloud.google.com/auth/branding?project=$project
3. OAuth client of type Desktop app:
   https://console.cloud.google.com/auth/clients/create?project=$project

Then publish this config where your users can fetch it, such as an
intranet page, with the client ID and secret filled in:

   {
     "client_id": "<ID>",
     "client_secret": "<SECRET>",
     $live
   }

Users install it with: mutter config import <file or https URL>
EOF

if [[ $per_user == true ]]; then
	cat <<EOF

Per-user topics: run the provisioning script from docs/organizations.md on a
schedule, so every member of $group gets a topic and subscription. If an
earlier shared setup granted $group roles/pubsub.editor on the project,
remove it, or the isolation does nothing.
EOF
fi
