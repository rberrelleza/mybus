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
DYNAMO_ENDPOINT = os.getenv("DYNAMO_ENDPOINT", None)
DYNAMO_REGION = os.getenv("DYNAMO_REGION", "us-east-1")
TIME_TEMPLATE = "Bus {0} is coming in {1} minutes"
STOP_TEMPLATE = "At {} {}"
SORRY = "Sorry, I'm having problems right now, please try again later"

app = Flask(__name__)
ask = Ask(app, "/")
logging.getLogger("flask_ask").setLevel(logging.DEBUG)


if DYNAMO_ENDPOINT:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION, endpoint_url=DYNAMO_ENDPOINT)
else:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION)

dynamodb_table = dynamodb.Table("sfbus")

@ask.launch
def getBusTimes():
    card_title = render_template('card_title')
    reponseStatement = None

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if not response or len(response["Items"]) == 0:
      reponseStatement = "Please add a stop first"
    else:
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
              readable_departure_times = "{}".format(d.times[0])
              if len(d.times) > 1:
                readable_departure_times = "{} and {}".format(readable_departure_times, d.times[1])

          departures.append(TIME_TEMPLATE.format(d.route, readable_departure_times))
        stop_texts.append(STOP_TEMPLATE.format(stop.name, ", ".join(departures)))

      if len(departures) == 0:
          reponseStatement = "Couldn't get information about the requested stops, please try again"
      else:
          reponseStatement = "; ".join(stop_texts)


    return statement(reponseStatement).simple_card(card_title, "\n".join(stop_texts))

@ask.intent("AddStop")
def addStop(StopID):
    if StopID is None:
      return statement(SORRY)

    stop = Stop(TOKEN, StopID, StopID)
    try:
      stop.load()
    except Exception as ex:
      logging.exception("error loadding the stop")
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

    card_title = render_template('card_title')
    responseText = render_template("add_stop", stop=stop.name)
    return statement(responseText).simple_card(card_title, responseText)

@ask.intent("RemoveStop")
def removeStop(StopID):
    if StopID is None:
      return statement(SORRY)

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))

    if not response or len(response["Items"]) == 0 or 'stops' not in response["Items"][0]:
      return statement("Please add a stop first")

    stops = response["Items"][0]['stops']
    if StopID in stops:
      stops.pop(StopID, None)
      response = dynamodb_table.update_item(
          Key={
              'userId': session.user.userId
          },
          UpdateExpression="set stops = :s",
          ExpressionAttributeValues={
              ':s': stops
          }
      )

    logging.info("Removed stop for user {}".format(session.user.userId))
    card_title = render_template('card_title')
    responseText = render_template("remove_stop", stop=stop.name)
    return statement("Ok").simple_card(card_title, responseText)

@ask.intent("ListStops")
def listStops():
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if not response:
      return statement("You don't have any stops")

    stops = response["Items"][0]['stops']
    stop_ids = [s for s in stops]

    card_title = render_template('card_title')
    responseText = render_template("list_stops", stops=", ".join(stop_ids))
    return statement(responseText).simple_card(card_title, responseText)


if __name__ == '__main__':
    if not TOKEN:
      raise Exception("Set the FIVEONEONE_TOKEN env var before launching")
    app.run(debug=True)