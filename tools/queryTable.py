import boto3
from boto3.dynamodb.conditions import Key

dynamodb = boto3.resource('dynamodb', region_name='us-west-2', endpoint_url="http://localhost:8000")

table = dynamodb.Table('sfbus')
response = table.query(KeyConditionExpression=Key('userId').eq('foo'))
print(response)