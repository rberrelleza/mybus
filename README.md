# Where's my bus?

An Alexa skill that tells you when the next **SF Muni** or **AC Transit** bus is
coming to your saved stops, using real-time predictions from
[511.org](https://511.org/open-data/transit).

> "Alexa, open my bus" · "Alexa, ask my bus when's my next bus" · "Alexa, ask my bus to add stop 15651"

## What you can say

| Say | What happens |
| --- | --- |
| "next bus", "when's my next bus" | Up to three upcoming arrivals for every bus you follow |
| "add stop 15651" | Looks the stop up on Muni and AC Transit. If several lines stop there, Alexa asks which one |
| "add the 38 at stop 15651", "add AC Transit stop 55555" | Adds a bus in one go, or picks the agency |
| "list my buses" | What you follow, grouped by stop |
| "remove bus 38", "remove stop 15651" | Stop following a bus or a whole stop |

Stop numbers are printed on bus stop signs and shelters.

## Layout

```
main.go                     Lambda runtime or HTTPS server (auto-detected)
internal/alexa              Request/response types, ElicitSlot, request signature verification
internal/skill              Intent handlers and speech
internal/transit            511.org StopMonitoring client (Muni = SF, AC Transit = AC)
internal/store              Saved stops in DynamoDB (same table/layout as v1)
skill-package/              Skill manifest, interaction model and icons (ASK CLI v2 layout)
```

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `FIVEONEONE_TOKEN` | — | **Required.** [511.org API key](https://511.org/open-data/token) |
| `SKILL_ID` | — | Skill ID from the developer console. Requests for other skills are rejected (`APPLICATION_ID` also works) |
| `DYNAMO_TABLE` | `sfbus` | DynamoDB table (partition key `userId`, string) |
| `DYNAMO_REGION` | `us-east-1` | |
| `DYNAMO_ENDPOINT` | — | For DynamoDB Local |
| `DYNAMO_CREATE_TABLE` | `false` | Create the table on startup (local development) |
| `PORT` | `8080` | HTTP mode only |
| `SKIP_REQUEST_VERIFICATION` | `false` | HTTP mode only. Disables Alexa signature checks for local testing. **Never in production** |
| `LOGLEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |

511.org limits each key to 60 requests per hour by default. The skill makes one
request per saved stop and caches each one for 30 seconds.

## Run and deploy

### Tests

```bash
go test ./...
```

### Locally or on Okteto

```bash
FIVEONEONE_TOKEN=... SKILL_ID=... docker compose up --build
```

```bash
okteto deploy
```

```bash
okteto up mybus
```

In HTTP mode the binary is an Alexa
[web service endpoint](https://developer.amazon.com/en-US/docs/alexa/custom-skills/host-a-custom-skill-as-a-web-service.html).
It checks the `Signature-256` / `SignatureCertChainUrl` headers and the request
timestamp on every call. Alexa needs a public HTTPS URL with a valid certificate.
Set it as `apis.custom.endpoint.uri` in `skill-package/skill.json`.

### AWS Lambda

The same binary runs on the `provided.al2023` runtime:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap . && zip mybus.zip bootstrap
```

Add an Alexa Skills Kit trigger with your skill ID, give the function's role
`dynamodb:GetItem`, `UpdateItem` (and `CreateTable` if `DYNAMO_CREATE_TABLE` is
set) on the table, and set the endpoint in `skill.json` to the function ARN
(`"endpoint": {"uri": "arn:aws:lambda:..."}`).

### Skill package

The manifest and interaction model use the ASK CLI v2 layout:

```bash
ask deploy --target skill-metadata
```

## Upgrading from v1 (Python/Flask-Ask)

- The old 511 API (`services.my511.org/Transit2.0`) has been shut down, so v1
  could no longer return arrival times. v2 uses `api.511.org`. You may need a
  new API key from https://511.org/open-data/token.
- Saved stops carry over. The DynamoDB table and item layout are unchanged;
  stops saved by v1 are read as Muni stops.
- Intents were renamed (`GetMyBus` → `GetBusTimesIntent`, …) and AddBus was
  folded into `AddStopIntent`. Deploy the new interaction model together with
  the new backend.
