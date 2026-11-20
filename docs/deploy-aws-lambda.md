# Deploying My Bus to AWS Lambda

My Bus runs on AWS Lambda as one Go binary on the `provided.al2023` runtime (arm64), in `us-east-1`. The 511.org API key lives in AWS Secrets Manager and the function reads it once per cold start. Saved stops stay in the existing `sfbus` DynamoDB table.

On each request, Alexa invokes the function directly through an Alexa Skills Kit trigger. That trigger checks the skill ID, so the HTTPS signature checks used in web-service mode are not needed here. The function then reads the user's stops from DynamoDB and fetches arrivals from 511.org.

Once set up, every push to `main` that passes the tests deploys itself through GitHub Actions (section 6). The rest of this guide is the one-time setup, and the manual steps for the rare changes the workflow doesn't cover.

## Overview

What you need before starting:

| Item | Notes |
| --- | --- |
| AWS CLI v2 | Configured for the target account (`aws sts get-caller-identity` works) |
| Go 1.26 | To build the binary. An older Go downloads 1.26 automatically on the first build |
| ASK CLI | `npm install -g ask-cli`, then `ask configure` with your Amazon developer account |
| 511.org API key | Free from [511.org/open-data/token](https://511.org/open-data/token) |
| Skill ID | Alexa developer console → your skill → **Copy Skill ID** |

Use `us-east-1`: for skills in the US, Alexa calls the North America Lambda endpoint there. The commands below use these variables; set them once per terminal:

```bash
export AWS_PROFILE=your-profile
export REGION=us-east-1
export SKILL_ID=amzn1.ask.skill.YOUR_SKILL_ID
export ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
```

`AWS_PROFILE` is the AWS CLI profile holding your credentials; drop it if you use the default profile. Skills created before 2017 have IDs in the older `amzn1.echo-sdk-ams.app.…` format, which works everywhere below.

Use an IAM user or IAM Identity Center user with admin rights for this setup, not the account's root user. Root access keys can't be limited and can close the account. After setup, day-to-day deploys go through GitHub Actions and need no personal keys.

## 1. Build the deployment zip

Lambda's `provided.al2023` runtime runs an executable named `bootstrap`, so the build names the binary that. From the repository root:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o bootstrap . && zip -j mybus.zip bootstrap
```

Check it with `file bootstrap`, which should say `ELF 64-bit LSB executable, ARM aarch64`. The binary detects Lambda by the `AWS_LAMBDA_RUNTIME_API` variable the runtime sets, so the same code also runs as a web service elsewhere. `bootstrap` and `mybus.zip` are git-ignored.

## 2. IAM role and DynamoDB table

The function runs as its own IAM role, allowed to write logs and read and write only the `sfbus` table.

```bash
aws iam create-role --role-name mybus-lambda --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
```

```bash
aws iam attach-role-policy --role-name mybus-lambda --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
```

```bash
aws iam put-role-policy --role-name mybus-lambda --policy-name sfbus-table --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"dynamodb:GetItem\",\"dynamodb:UpdateItem\"],\"Resource\":\"arn:aws:dynamodb:${REGION}:${ACCOUNT_ID}:table/sfbus\"}]}"
```

Check whether the original `sfbus` table still exists:

```bash
aws dynamodb describe-table --region $REGION --table-name sfbus --query "Table.[TableStatus,ItemCount,KeySchema]"
```

If it prints `ACTIVE` with `userId` as the key, keep it: existing users keep their saved stops. Only if it says `ResourceNotFoundException`, create it:

```bash
aws dynamodb create-table --region $REGION --table-name sfbus --attribute-definitions AttributeName=userId,AttributeType=S --key-schema AttributeName=userId,KeyType=HASH --billing-mode PAY_PER_REQUEST
```

**Back up an existing table before the first deploy.** The new code rewrites a user's entry the first time they add or remove something: keys change from `15651` to `SF:15651`, and an `agency` field is added. It reads old entries fine, but the old Python version can't read the new format, so a backup is your rollback:

```bash
aws dynamodb create-backup --region $REGION --table-name sfbus --backup-name sfbus-before-go-rewrite
```

Optionally, also turn on point-in-time recovery, which can restore the table to any second in the last 35 days:

```bash
aws dynamodb update-continuous-backups --region $REGION --table-name sfbus --point-in-time-recovery-specification PointInTimeRecoveryEnabled=true
```

## 3. Store the 511 API key in Secrets Manager

The key goes in a Secrets Manager secret, not a Lambda environment variable, so it never shows in the function's configuration, in the console, or in `get-function` output. The function reads it once per cold start and keeps it in memory.

Create the secret and keep its ARN:

```bash
SECRET_ARN=$(aws secretsmanager create-secret --region $REGION --name mybus/511-api-key --description "511.org API key for the My Bus Alexa skill" --secret-string 'YOUR_511_KEY' --query ARN --output text) && echo $SECRET_ARN
```

This leaves the key in your shell history. To avoid that, create the secret in the AWS console instead: Secrets Manager → **Store a new secret** → **Other type of secret**, named `mybus/511-api-key`. Then run `export SECRET_ARN=` followed by the ARN the console shows.

The secret can hold the key as plain text, as above, or as JSON `{"FIVEONEONE_TOKEN": "…"}`. JSON is what the console creates by default when you choose key/value pairs. Both formats work.

Allow the function's role to read only this secret:

```bash
aws iam put-role-policy --role-name mybus-lambda --policy-name read-511-key --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"secretsmanager:GetSecretValue\",\"Resource\":\"${SECRET_ARN}\"}]}"
```

Check the role now has all its permissions. The first list should show `read-511-key` and `sfbus-table`, the second `AWSLambdaBasicExecutionRole`:

```bash
aws iam list-role-policies --role-name mybus-lambda --query PolicyNames && aws iam list-attached-role-policies --role-name mybus-lambda --query "AttachedPolicies[].PolicyName"
```

The function finds the secret through the `FIVEONEONE_TOKEN_SECRET` variable, set in the next section to the secret's name or ARN. When both it and `FIVEONEONE_TOKEN` are set, the secret wins. The secret uses the default AWS-managed encryption key, which needs no extra permission. A customer-managed KMS key would also need `kms:Decrypt` on the role.

Cost is roughly $0.40 per secret per month plus $0.05 per 10,000 reads (approximate; check current Secrets Manager pricing). The skill reads the secret only on cold starts, so read costs are negligible.

## 4. Create the function and the Alexa trigger

Create the function with the secret's name in `FIVEONEONE_TOKEN_SECRET`. An 8-second timeout matches how long Alexa waits for a response.

```bash
aws lambda create-function --region $REGION --function-name mybus --runtime provided.al2023 --architectures arm64 --handler bootstrap --zip-file fileb://mybus.zip --role arn:aws:iam::${ACCOUNT_ID}:role/mybus-lambda --timeout 8 --memory-size 128 --environment "Variables={FIVEONEONE_TOKEN_SECRET=mybus/511-api-key,SKILL_ID=${SKILL_ID}}"
```

Right after creating the role, this can fail with "The role defined for the function cannot be assumed by Lambda". IAM takes a few seconds to propagate; wait and run it again.

Then let Alexa invoke the function, for your skill only:

```bash
aws lambda add-permission --region $REGION --function-name mybus --statement-id alexa-skill --action lambda:InvokeFunction --principal alexa-appkit.amazon.com --event-source-token $SKILL_ID
```

The `--event-source-token` restricts the trigger to your skill ID. The function also rejects requests for any other skill ID through `SKILL_ID`.

### Test the function before connecting Alexa

Send the function two fake Alexa requests. The first checks the secret and DynamoDB; the second calls the live 511 API. Neither saves anything.

```bash
aws lambda wait function-active-v2 --region $REGION --function-name mybus
```

```bash
cat > launch.json <<EOF
{"version":"1.0","context":{"System":{"application":{"applicationId":"${SKILL_ID}"},"user":{"userId":"amzn1.ask.account.cli-test"}}},"request":{"type":"LaunchRequest","requestId":"cli-1","timestamp":"2026-11-19T00:00:00Z","locale":"en-US"}}
EOF
```

```bash
aws lambda invoke --region $REGION --function-name mybus --cli-binary-format raw-in-base64-out --payload file://launch.json out.json && cat out.json
```

The response should contain "Welcome to My Bus". Then add a stop:

```bash
cat > addstop.json <<EOF
{"version":"1.0","context":{"System":{"application":{"applicationId":"${SKILL_ID}"},"user":{"userId":"amzn1.ask.account.cli-test"}}},"request":{"type":"IntentRequest","requestId":"cli-2","timestamp":"2026-11-19T00:00:00Z","locale":"en-US","intent":{"name":"AddStopIntent","slots":{"StopID":{"name":"StopID","value":"15651"}}}}}
EOF
```

```bash
aws lambda invoke --region $REGION --function-name mybus --cli-binary-format raw-in-base64-out --payload file://addstop.json out.json && cat out.json
```

It should list the lines at Market St & 8th St with their destinations, for example "the 7 to Transit Center Bay E". If either reply says "something went wrong" or "can't reach the 511 transit service", the logs say why:

```bash
aws logs tail /aws/lambda/mybus --region $REGION --since 10m
```

The test files are git-ignored; delete them when you're done with `rm launch.json addstop.json out.json`.

## 5. Point the skill at Lambda

`skill-package/skill.json` in the repo already points at the function. If you deploy to a different account, region or function name, change its endpoint to your function's ARN. Lambda endpoints take no `sslCertificateType`:

```json
"endpoint": { "uri": "arn:aws:lambda:us-east-1:ACCOUNT_ID:function:mybus" }
```

**Target the existing skill.** Without this file, `ask deploy` creates a brand-new skill instead of updating the one people already use. The `.ask/` folder is git-ignored:

```bash
mkdir -p .ask && cat > .ask/ask-states.json <<EOF
{"askcliStatesVersion":"2020-03-31","profiles":{"default":{"skillId":"${SKILL_ID}"}}}
EOF
```

If your ASK CLI profile isn't `default`, use its name in the file and add `-p <profile>` to the deploy. Then deploy the manifest and interaction model. The output should say "Using pre-existing skill id" followed by your ID:

```bash
ask deploy --target skill-metadata
```

This only updates the skill's development version. The live skill doesn't change until it passes certification (section 8). GitHub Actions (section 6) only deploys the function code, so rerun this command whenever `skill-package/` changes.

## 6. Deploy automatically with GitHub Actions (OIDC)

After this one-time setup, every push to `main` that passes the tests deploys itself to Lambda, and no AWS keys are stored in GitHub. The `deploy` job in `.github/workflows/ci.yml` does it this way:

1. The job runs in the `production` GitHub environment and asks GitHub for a short-lived OIDC token.
2. `aws-actions/configure-aws-credentials` trades that token with AWS STS for temporary credentials of a deploy role.
3. AWS issues them only if the token says it comes from `rberrelleza/mybus`, environment `production`.
4. The job builds `bootstrap`, runs `update-function-code`, and waits until the update finishes.

The deploy role can only update this one function's code. It can't read the 511 secret, change IAM, or touch DynamoDB.

### Register GitHub as an identity provider in AWS

Once per AWS account:

```bash
aws iam create-open-id-connect-provider --url https://token.actions.githubusercontent.com --client-id-list sts.amazonaws.com
```

`EntityAlreadyExists` means another project already created it, which is fine. Older AWS CLI versions require a thumbprint; if yours asks for one, add `--thumbprint-list 6938fd4d98bab03faadb97b34396831e3780aea1`. AWS doesn't rely on it for GitHub's provider.

### Create the deploy role

The trust policy lets only the `production` environment of this repository assume the role:

```bash
cat > github-trust.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::${ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {"StringEquals": {
      "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
      "token.actions.githubusercontent.com:sub": "repo:rberrelleza/mybus:environment:production"
    }}
  }]
}
EOF
```

```bash
aws iam create-role --role-name mybus-github-deploy --assume-role-policy-document file://github-trust.json --query Role.Arn --output text
```

```bash
aws iam put-role-policy --role-name mybus-github-deploy --policy-name deploy-mybus --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"lambda:UpdateFunctionCode\",\"lambda:GetFunction\",\"lambda:GetFunctionConfiguration\"],\"Resource\":\"arn:aws:lambda:${REGION}:${ACCOUNT_ID}:function:mybus\"}]}"
```

Check that the permission is attached (`["deploy-mybus"]`), then delete the trust policy file so it isn't committed:

```bash
aws iam list-role-policies --role-name mybus-github-deploy --query PolicyNames && rm github-trust.json
```

### Configure the repository

1. In GitHub, go to Settings → Environments → **New environment**, and name it `production`. Under "Deployment branches and tags", choose **Selected branches and tags** and add a rule for `main` only. You can also add yourself as a required reviewer to approve each deploy.
2. Under Settings → Secrets and variables → Actions → **Variables**, add these repository variables. They are variables, not secrets, because none of them is sensitive:

| Variable | Value |
| --- | --- |
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::<ACCOUNT_ID>:role/mybus-github-deploy` |
| `AWS_REGION` | `us-east-1` (default if unset) |
| `LAMBDA_FUNCTION_NAME` | `mybus` (default if unset) |

Until `AWS_DEPLOY_ROLE_ARN` is set, the deploy job is skipped and CI only runs tests. Once it's set, the next push to `main` deploys. In the Actions tab, that run shows a `deploy` job after `test`, about 35 seconds long, whose "configure-aws-credentials" step proves the OIDC login works. The workflow deploys code only. The skill package (section 5) and the secret (section 3) are still changed by hand, which is rare.

If a deploy fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity", the trust policy's `sub` doesn't match. Check that the job ran in the `production` environment and that the repository name matches exactly.

## 7. Test, logs, updates and key rotation

**Test.** In the [Alexa developer console](https://developer.amazon.com/alexa/console/ask), open the skill. In the bar at the top (Build, Test, Distribution, Certification, Analytics), click **Test**, and set "Skill testing is enabled in" to **Development**. Type into the Alexa Simulator box. The JSON Input and Output panels on the right show what the function received and returned; if they stay empty, the request never reached the skill.

| Say | Result |
| --- | --- |
| ask my bus to add stop 15651 | Reaches the skill |
| ask my bus when's my next bus | Reaches the skill |
| open my bus skill | Reaches the skill |
| open / launch / start my bus | Doesn't reach it: Alexa answers "bus is not supported on this device" |
| when's my next bus (no "my bus") | Doesn't reach it: Alexa's built-in transit feature answers |

The store's example phrases use only the phrasings that work. Once testing is enabled, `ask dialog --locale en-US` runs the same simulator from the terminal.

**Logs.** The function writes JSON logs to CloudWatch, in the log group `/aws/lambda/mybus`. To follow them live:

```bash
aws logs tail /aws/lambda/mybus --region $REGION --follow
```

A cold start that can't read the secret fails with `reading secret mybus/511-api-key: …`. That usually means the `read-511-key` policy is missing or names the wrong ARN.

**Update the code by hand** (when not using GitHub Actions): rebuild the zip (section 1), then:

```bash
aws lambda update-function-code --region $REGION --function-name mybus --zip-file fileb://mybus.zip
```

**Rotate the 511 key.** Store the new value, then force the function to restart so it reads it:

```bash
aws secretsmanager put-secret-value --region $REGION --secret-id mybus/511-api-key --secret-string 'NEW_511_KEY'
```

```bash
aws lambda update-function-configuration --region $REGION --function-name mybus --description "key rotated $(date -u +%F)"
```

The function caches the key until its environment is recycled, and any configuration change recycles it. Keep the old 511 key valid until the update has finished.

## 8. Submit for certification

Users keep getting the live version until a new one passes Amazon's review, usually a few days. Code changes deployed to Lambda reach users right away, because both versions call the same function. Changes to `skill-package/` (phrases, model, listing) need a new submission.

1. **Distribution** tab: check the Skill Preview (example phrases, description), Privacy & Compliance, and Availability (public, US).
2. **Certification** tab → **Validation** → **Run**. Fix anything it lists, redeploy with `ask deploy --target skill-metadata`, and run it again.
3. **Functional test** → **Run**.
4. **Submission**: choose **Certify and publish now**, then **Submit for review**.

While it's under review, the skill list shows two rows with the same name: the **Live** version and the **In Review** one. That's normal. On approval the new version replaces the live one; a rejection email lists what to fix.

If you used the root user for setup, deactivate and delete its access key afterwards, under your account name → **Security credentials**.
