# Running mutter in an organization

What an organization takes on when it adopts mutter: setup, Pub/Sub cost and the standing with Google.
Written 2026-10-06 against commit `6f33aae`.

## Setup effort

### Today: shared topic

- One GCP project, one run of `scripts/setup.sh`, three console steps (Chat app config, Internal OAuth branding, Desktop OAuth client).
- The admin publishes a config with the client and topic. Users install the public release and run `mutter config import`.
- Users only log in. Each client creates its own Workspace Events subscriptions and filtered Pub/Sub subscriptions (`ensurePubsubSub` in `events.go`).
- Pub/Sub subscriptions unused for 31 days delete themselves.
- Without a topic, mutter still works and refreshes when a space is opened.

### Isolation needs admin provisioning

- On the shared topic, any member of the group can attach an unfiltered subscription and see every user's event metadata (see [Isolating users](../README.md#isolating-users)).
- Per-user isolation can't be self-service:
    - Attaching a subscription needs `pubsub.topics.attachSubscription` on the topic. Granting it project-wide restores the exposure.
    - IAM Conditions can't tie a resource-name prefix to the caller's identity.
- An admin must create a topic and subscription per user. [Provisioning per-user topics](#provisioning-per-user-topics) automates this.
- New joiners have no live updates until the job runs.

## Provisioning per-user topics

mutter doesn't support this yet. It still uses the shared topic and creates its own subscriptions. This section describes what the admin side looks like once mutter supports per-user topics, following the plan in [Isolating users](../README.md#isolating-users).

### Resources per user

| Resource | Name | IAM |
|---|---|---|
| Topic | `mutter-user-<slug>` | `chat-api-push@system.gserviceaccount.com` has `roles/pubsub.publisher` |
| Subscription | `mutter-user-<slug>`, on the topic above | the user has `roles/pubsub.subscriber` |

- `<slug>` is the lowercased email with every character outside `a-z0-9` replaced by `-`, so `Kim.N@netic.dk` becomes `kim-n-netic-dk`. mutter must derive the same name.
- The slug can collide, for example `a.b@x.dk` and `a-b@x.dk`. Rename one by hand if it happens.
- One subscription per user carries both spaces and read state, because a per-user topic needs no filter.
- Subscriptions never expire. The script removes them when the user leaves the group.
- Remove the group's project-wide `roles/pubsub.editor` that `setup.sh` grants, or the isolation does nothing.

### Script

The script is idempotent and reconciles the project with the group. It creates resources for new members and deletes resources for anyone no longer in the group. It runs on the stock macOS bash 3.2.

```bash
#!/usr/bin/env bash
# Gives each user in GROUP a topic and subscription, and removes those of users who left.
set -euo pipefail

if [[ $# -ne 2 ]]; then
	echo "usage: $0 PROJECT_ID USERS_GROUP_EMAIL" >&2
	exit 1
fi
project=$1
group=$2
prefix=mutter-user-

# Must match the name mutter derives from the user's email.
slug() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9' '-'; }

emails=$(gcloud identity groups memberships list --group-email "$group" \
	--view full --filter 'type=USER' --format 'value(preferredMemberKey.id)')
want=""
for email in $emails; do
	name=$prefix$(slug "$email")
	want="$want $name"
	gcloud pubsub topics describe "$name" --project "$project" >/dev/null 2>&1 && continue

	gcloud pubsub topics create "$name" --project "$project"
	gcloud pubsub topics add-iam-policy-binding "$name" --project "$project" \
		--member serviceAccount:chat-api-push@system.gserviceaccount.com \
		--role roles/pubsub.publisher >/dev/null
	gcloud pubsub subscriptions create "$name" --project "$project" \
		--topic "$name" --ack-deadline 60 --expiration-period never
	gcloud pubsub subscriptions add-iam-policy-binding "$name" --project "$project" \
		--member "user:$email" --role roles/pubsub.subscriber >/dev/null
done

for name in $(gcloud pubsub topics list --project "$project" \
	--filter "name:topics/$prefix" --format 'value(name.basename())'); do
	[[ " $want " == *" $name "* ]] && continue
	gcloud pubsub subscriptions delete "$name" --project "$project" --quiet || true
	gcloud pubsub topics delete "$name" --project "$project" --quiet
done
```

- `memberships list` returns direct members only. If the group contains other groups, use `gcloud identity groups memberships search-transitive-memberships` instead.
- A topic that already exists is skipped as a whole. If a run fails halfway, delete that user's topic and run the script again.

### Running it on a schedule

The script only needs `gcloud`, so any scheduler that can authenticate to Google Cloud can run it:

| Runner | Identity |
|---|---|
| Cloud Scheduler triggering a Cloud Run job | a service account attached to the job |
| GitHub Actions `schedule` workflow | a service account through Workload Identity Federation, with no key file |
| An admin's machine | the admin's own login, run by hand after onboarding |

The service account needs two grants:

- `roles/pubsub.admin` on the project, to create and delete topics and subscriptions and set IAM on them.
- Read access to the group's members. Assign it the Groups Reader admin role in the Admin console, or make it a member of the group with member visibility.

A daily run is enough. Run it by hand to give a new joiner live updates the same day.

### Unverified

- The script's syntax checks out with `bash -n`, but it hasn't been run against a real project.
- It's unknown whether `type=USER` filters as expected with `--view full`. Check the output of the `memberships list` line on its own first.
- The Workspace Events API may require the user to have access to the topic when they create a subscription to it. If creation fails with a permission error, also grant the user `roles/pubsub.viewer` on their own topic.
- The Groups Reader route for a service account comes from memory of Cloud Identity behaviour.

## Pub/Sub cost

### Pricing

- Throughput costs $40/TiB, after 10 GiB free per month.
- Each publish, pull or push request is billed at least 1 KB.
- Messages dropped by a subscription filter are still billed for delivery: "You incur message delivery fees … for these messages." ([filter docs](https://docs.cloud.google.com/pubsub/docs/subscription-message-filter))

### Shared topic cost grows with users²

Every event on the shared topic is billed once per attached subscription. Each running machine attaches two subscriptions, one for spaces and one for read state.

Monthly delivery bytes ≈ users × events per user per day × 30 × subscriptions × 1 KB, with subscriptions = 2 × machines.

### Estimate

The inputs are assumptions, not measurements: 1,000 events per user per day (messages, edits, reactions, read state), 1.5 machines per user, 1 KB billed per event.

| Users | Shared topic | Per-user topics |
|---|---|---|
| 20 | ~$1/mo | free tier |
| 50 | ~$9/mo | free tier |
| 200 | ~$130/mo | free tier |
| 1000 | ~$3,300/mo | ~$1/mo |

- The cost scales linearly with events per user. Measure the real rate from one subscription's metrics in the Cloud console.
- Below about 50 users, the cost can be ignored.
- Above a few hundred users, per-user topics are needed for cost as well as isolation.

## Google terms and policy

- mutter calls only documented APIs (Chat, Workspace Events, Pub/Sub) with the user's own OAuth token. It doesn't reverse-engineer the Chat web client.
- Publishing the source is fine, because each organization brings its own project and OAuth client.
- An Internal OAuth audience limits logins to the organization and skips Google's app verification.
- One shared OAuth client for all organizations would need an External audience and OAuth verification for the Chat scopes. If Google classifies any of those scopes as restricted, it would also need a yearly security assessment. Per-org setup avoids both.

### Open risks (not verified)

- Workspace admins can restrict third-party API access. The internal app may need to be marked trusted in the Admin console.
- Chat API quotas apply per project, so every user in an organization shares one project's quota.
- Google's API terms forbid branding that implies Google made the client. Keep Google Chat logos out of the UI and the cask.

## Sources

- [Pub/Sub pricing](https://cloud.google.com/pubsub/pricing)
- [Pub/Sub subscription filters](https://docs.cloud.google.com/pubsub/docs/subscription-message-filter)
