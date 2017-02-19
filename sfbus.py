import logging
import os

import boto3
from boto3.dynamodb.conditions import Key
from fiveoneone.route import Route
from fiveoneone.stop import Stop
from fiveoneone.agency import Agency
from flask import Flask, render_template
from flask_ask import Ask, statement, question, session

TOKEN = os.getenv("FIVEONEONE_TOKEN")
DYNAMO_ENDPOINT = os.getenv("DYNAMO_ENDPOINT", "http://localhost:8000")
TIME_TEMPLATE = "Bus {0} is coming in {1} minutes"
STOP_TEMPLATE = "At {} {}"
SORRY = "Sorry, I'm having problems right now, please try again later"

app = Flask(__name__)
ask = Ask(app, "/")
logging.getLogger("flask_ask").setLevel(logging.DEBUG)

dynamodb = boto3.resource('dynamodb', region_name='us-west-2', endpoint_url=DYNAMO_ENDPOINT)
dynamodb_table = dynamodb.Table("sfbus")

@ask.launch
def getBusTimes():
    # TODO get this from a configuration
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if not response or len(response["Items"]) == 0:
      return statement("Please add a stop first")

    stops = []
    stop_texts = []
    for key in response["Items"][0]['stops']:
      s = response["Items"][0]['stops'][key]
      logging.error(s)
      stop = Stop(TOKEN, s["name"], s["code"])

      if "route" in s:
        deps = stop.next_departures(s["route"])
      else:
        deps = stop.all_departures()
      departures = []
      for d in deps:
        if len(d.times) > 0:
            readable_departure_times = "{} and {}".format(
                ", ".join([str(t) for t in d.times[:-1]]),
                            d.times[-1])
        departures.append(TIME_TEMPLATE.format(d.route, readable_departure_times))
      stop_texts.append(STOP_TEMPLATE.format(stop.name, ", ".join(departures)))

    if len(departures) == 0:
        return statement("Couldn't get information about the requested stops, please try again")
    else:
        return statement("; ".join(stop_texts))


@ask.intent("AddStop")
def addStop(StopID):
    if StopID is None:
      return statement(SORRY)

    stop = Stop(TOKEN, StopID, StopID)
    try:
      stop.load()
    except Exception as ex:
      logging.exception()
      return statement("I can't seem to find stop {} on my lists, please try again".format(StopID))

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if response and len(response["Items"]) > 0:
      stops = response["Items"][0]['stops']
    else:
      stops = {}

    stops[stop.code] = {
      'code': stop.code,
      'name': stop.name,
    }

    response = dynamodb_table.update_item(
        Key={
            'userId': session.user.userId
        },
        UpdateExpression="set stops = :s",
        ExpressionAttributeValues={
            ':s': stops
        }
    )

    logging.info("Set stop for user {}".format(session.user.userId))
    return statement("I added stop id {} to your list of stops".format(StopID))

@ask.intent("RemoveStop")
def removeStop(StopID):
    if StopID is None:
      return statement(SORRY)

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if not response or len(response["Items"]) == 0:
      return statement("Please add a stop first")

    stops = response["Items"][0]['stops']
    if StopID in stops:
      updated_stops = [s for s in stops if s['code'] != StopID]
      response = dynamodb_table.update_item(
          Key={
              'userId': session.user.userId
          },
          UpdateExpression="set stops = :s",
          ExpressionAttributeValues={
              ':s': updated_stops
          }
      )

    logging.info("Removed stop for user {}".format(session.user.userId))
    return statement("Ok")

@ask.intent("ListStops")
def listStops():
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if not response:
      return statement("You don't have any stops")

    stops = response["Items"][0]['stops']
    stop_ids = [s for s in stops]

    return statement("Your stops are {}".format(", ".join(stop_ids)))


if __name__ == '__main__':
    if not TOKEN:
      raise Exception("Set the FIVEONEONE_TOKEN env var before launching")
    app.run(debug=True)