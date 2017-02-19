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
NO_ROUTES = "Please add a bus stop first by saying 'Alexa, open my bus and add stop 15419"
TIME_TEMPLATE = "Bus {0} is coming in {1} minutes"
STOP_TEMPLATE = "At {} {}"
SORRY = "Sorry, I'm having problems right now, please try again later"
STOPID_KEY="stopid"
STOPNAME_KEY="stopname"

app = Flask(__name__)
ask = Ask(app, "/")
logging.getLogger("flask_ask").setLevel(logging.DEBUG)


if DYNAMO_ENDPOINT:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION, endpoint_url=DYNAMO_ENDPOINT)
else:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION)

dynamodb_table = dynamodb.Table("sfbus")


def isResponseEmpty(response):
  if not response or len(response["Items"]) == 0 or "stops" not in response["Items"][0] or \
    len(response["Items"][0]["stops"]) == 0:
      return True

  return False

def updateStopList(userId, newStop):
  response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(userId))

  if response and len(response["Items"]) > 0:
    stops = response["Items"][0]['stops']
  else:
    stops = {}

  if newStop['code'] in stops:
    existingStop = stops[newStop['code']]
    if 'buses' in existingStop:
      newStop['buses'] = list(set(existingStop['buses'] + newStop['buses']))

  stops[newStop['code']] = newStop

  response = dynamodb_table.update_item(
      Key={
          'userId':userId
      },
      UpdateExpression="set stops = :s",
      ExpressionAttributeValues={
          ':s': stops
      }
  )

  card_title = render_template('card_title')
  responseText = render_template("add_stop", stop=newStop['code'], route=",".join(newStop['buses']))
  return statement(responseText).simple_card(card_title, responseText)

def getSentence(words):
  if len(words) == 0:
    return ""
  elif len(words) == 1:
    return words[0]

  return "{} and {}".format(", ".join([str(w) for w in words[:-1]]), str(words[-1]))

@ask.launch
def getBusTimes():
    card_title = render_template('card_title')
    reponseStatement = None

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if isResponseEmpty(response):
      reponseStatement = NO_ROUTES
    else:
      stops = []
      departures = []

      for key in response["Items"][0]['stops']:
        s = response["Items"][0]['stops'][key]
        stop = Stop(TOKEN, s["name"], s["code"])
        if "buses" in s:
          for r in s["buses"]:
            d = stop.next_departures(r)
            readable_departure_times = getSentence(d.times)
            departures.append(TIME_TEMPLATE.format(d.route, readable_departure_times))

      if len(departures) == 0:
          reponseStatement = "Couldn't get information about the requested buses, please try again"
      else:
          reponseStatement = "; ".join(departures)

    return statement(reponseStatement).simple_card(card_title, reponseStatement)

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

    departures = stop.all_departures()
    buses = list(set(d.route for d in departures))

    if len(buses) == 1:
      newStop = dict(code=stop.code, name=stop.name, buses=[buses[0]])
      return updateStopList(session.user.userId, newStop)
    else:
      session.attributes[STOPID_KEY] = stop.code
      session.attributes[STOPNAME_KEY] = stop.name
      return question("Bus {} stops in stop {}. Please tell me which one to add by saying, add bus 9".format(getSentence(buses), StopID))

@ask.intent("AddBus")
def addBus(BusID):
  if BusID is None:
    return statement(SORRY)

  if STOPID_KEY not in session.attributes:
    return statement(SORRY)

  if STOPNAME_KEY not in session.attributes:
    return statement(SORRY)

  stop = Stop(TOKEN, session.attributes[STOPID_KEY], session.attributes[STOPID_KEY])
  departures = stop.all_departures()
  buses = list(set(d.route for d in departures))

  if BusID not in buses:
    return statement("I can't seem to find bus {} on my lists, please try again".format(BusID))
  else:
    newStop = dict(code=stop.code, name=session.attributes[STOPNAME_KEY], buses=[BusID])
    return updateStopList(session.user.userId, newStop)

@ask.intent("RemoveBus")
def removeBus(BusID):
    if BusID is None:
      return statement(SORRY)

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))

    if isResponseEmpty(response):
      return statement(NO_ROUTES)

    stops = response["Items"][0]['stops']

    deleteStop = None
    updateDynamo = False

    logging.debug("Initial stops {}".format(stops))
    for s in stops:
      if "buses" in stops[s] and BusID in stops[s]["buses"]:
        logging.debug("Deleting {} from stop {}".format(BusID, s))
        stops[s]["buses"].remove(BusID)
        logging.debug("Deleted {}? {}".format(BusID, stops[s]["buses"]))
        updateDynamo = True
        if len(stops[s]["buses"]) == 0:
          deleteStop = s
        break

    if deleteStop:
      stops.pop(deleteStop, None)

    if updateDynamo:
      logging.debug("Updating dynamo with {}".format(stops))
      response = dynamodb_table.update_item(
          Key={
              'userId': session.user.userId
          },
          UpdateExpression="set stops = :s",
          ExpressionAttributeValues={
              ':s': stops
          }
      )

    card_title = render_template('card_title')
    responseText = render_template("remove_bus", bus=BusID)
    return statement("Ok").simple_card(card_title, responseText)

@ask.intent("ListBuses")
def listBuses():
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if isResponseEmpty(response):
      return statement(NO_ROUTES)

    stops = response["Items"][0]['stops']
    print(stops)
    buses = []
    for s in stops:
      if 'buses' in stops[s] and len(stops[s]) > 0:
        buses += stops[s]["buses"]

    card_title = render_template('card_title')
    responseText = render_template("list_buses", buses=getSentence(buses))
    return statement(responseText).simple_card(card_title, responseText)


if __name__ == '__main__':
    if not TOKEN:
      raise Exception("Set the FIVEONEONE_TOKEN env var before launching")
    app.run(debug=True)