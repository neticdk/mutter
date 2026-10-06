# Running mutter in an organization

What an organization takes on when it adopts mutter: setup, Pub/Sub cost and the standing with Google.
Written 2026-10-06 against commit `6f33aae`.

## Setup effort

### Today: shared topic

- One GCP project, one run of `mutter admin setup`, and three console steps it walks through (Chat app config, Internal OAuth branding, Desktop OAuth client).
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

`mutter admin setup` provisions every member once when it sets up per-user topics. After that, run this on a schedule:

```sh
mutter admin provision PROJECT_ID USERS_GROUP_EMAIL
```

It reconciles the project with the group and is safe to run any time. It creates resources for people who joined, reapplies everyone's IAM, and deletes the resources of people who left.

### Resources per user

| Resource | Name | IAM |
|---|---|---|
| Topic | `mutter-user-<id>` | `chat-api-push@system.gserviceaccount.com` has `roles/pubsub.publisher`, the user has `roles/pubsub.viewer` |
| Subscription | `mutter-user-<id>`, on the topic above | the user has `roles/pubsub.subscriber` |

- `<id>` is the user's numeric Google account ID, the same ID Chat uses in `users/<id>`. It stays the same when an email changes, so names never need renaming.
- One subscription per user carries both spaces and read state, because a per-user topic needs no filter.
- Users can't create their own subscriptions, even on their own topic. Pulling from a subscription needs `pubsub.subscriptions.consume`, and a creator gets no permissions on what it creates, so self-service would need that permission project-wide, on everyone's subscriptions.
- Subscriptions never expire. Provisioning removes them when the user leaves the group.
- IAM names users by email. Each run sets every role to exactly the current address, which also drops an address from before an email change.
- The viewer role lets the user's own Workspace Events subscription reach the topic. It's untested whether the API needs it, and it exposes nothing.
- Remove the group's project-wide `roles/pubsub.editor` from an earlier shared setup, or the isolation does nothing.

### Several teams

Use one parent group, such as `mutter-users`, with the team groups as its members, and give provisioning the parent.

- Provisioning reads members transitively, so everyone in a nested team group gets a topic. Someone in two teams gets one.
- Team leads manage their own team group, and access follows on the next run without changes to provisioning.
- Provisioning deletes the resources of anyone missing from the group it's given. Running it once per team would delete the other teams' topics, so always give it the one parent group.
- Shared mode doesn't extend to several teams. Its group role covers the whole project, so every team could read every other team's event metadata. Teams that want their own shared topic set up their own project.

### Running it on a schedule

`mutter admin provision` uses the gcloud login when gcloud is installed, and Application Default Credentials otherwise. A scheduler needs only the mutter binary:

| Runner | Identity |
|---|---|
| Cloud Scheduler triggering a Cloud Run job | a service account attached to the job |
| GitHub Actions `schedule` workflow | a service account through Workload Identity Federation, with no key file |
| An admin's machine | the admin's own login, run by hand after onboarding |

The service account needs two grants:

- `roles/pubsub.admin` on the project, to create and delete topics and subscriptions and set IAM on them.
- Read access to the group's members. Assign it the Groups Reader admin role in the Admin console, or make it a member of the group with member visibility.

A daily run is enough. Run it by hand to give a new joiner live updates the same day.

### Verified and unverified

- Verified on 2026-10-06: Cloud Identity's transitive membership search lists the group's users as `users/<id>`, the same account IDs mutter derives names from.
- Verified on 2026-10-06, as the project owner: per-user live updates work end to end with provisioned resources.
- `mutter admin setup` and `mutter admin provision` are tested against a fake of the APIs, not yet against a real project.
- Whether a user who isn't a project owner needs the viewer role on their topic. Provisioning grants it either way.
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
