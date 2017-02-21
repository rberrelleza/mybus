import json
import logging
import os

import boto3
from boto3.dynamodb.conditions import Key
from fiveoneone.route import Route
from fiveoneone.stop import Stop
from fiveoneone.agency import Agency
from flask import Flask, render_template, request
from flask_ask import Ask, statement, question, session
from flask_ask import request as ask_request
from voicelabs import VoiceInsights

TOKEN = os.getenv("FIVEONEONE_TOKEN")
DYNAMO_ENDPOINT = os.getenv("DYNAMO_ENDPOINT", None)
DYNAMO_REGION = os.getenv("DYNAMO_REGION", "us-east-1")
LOGLEVEL = os.getenv("LOGLEVEL", "INFO")
STOPID_KEY="stopid"
STOPNAME_KEY="stopname"

app = Flask(__name__)
app.config["ASK_APPLICATION_ID"] = os.getenv("APPLICATION_ID")
ask = Ask(app, "/")

logging.getLogger("flask_ask").setLevel(LOGLEVEL)
logging.getLogger(__name__).setLevel(LOGLEVEL)
log = logging.getLogger(__name__)

if DYNAMO_ENDPOINT:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION, endpoint_url=DYNAMO_ENDPOINT)
else:
  dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION)

dynamodb_table = dynamodb.Table("sfbus")

vi_apptoken = os.getenv("VI_APPTOKEN")
vi = VoiceInsights()

def before_request():
    if vi_apptoken:
      vi.initialize(vi_apptoken, json.loads(request.data)['session'])

def after_request(response):
    if vi_apptoken:
      intent_name = ask_request.type
      if ask_request.intent:
        intent_name = ask_request.intent.name

      try:
        vi.track(intent_name,
              ask_request,
              json.loads(response.get_data())['response']['outputSpeech']['text'])
      except Exception as ex:
        log.exception("Failed to send analytics")

    return response

app.after_request(after_request)
app.before_request(before_request)

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
  responseText = render_template("add_stop_success", stop=newStop['code'], route=",".join(newStop['buses']))
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
    responseStatement = None

    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if isResponseEmpty(response):
      responseStatement = render_template("no_bus_stop")
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
            departures.append(dict(bus=d.route, departures=readable_departure_times))

      if len(departures) == 0:
          responseStatement = render_template("get_departures_failed")
      else:
          responseStatement = render_template("get_departures_success", departures=departures)

    return statement(responseStatement).simple_card(card_title, responseStatement)

@ask.intent("AddStop")
def addStop(StopID):
    if StopID is None:
      return statement(render_template("no_bus_stop"))

    stop = Stop(TOKEN, StopID, StopID)
    try:
      stop.load()
    except Exception as ex:
      log.exception("error loadding the stop")
      return statement("I can't seem to find stop {} on my lists, please try again".format(StopID))

    departures = stop.all_departures()
    buses = list(set(d.route for d in departures))

    if len(buses) == 1:
      newStop = dict(code=stop.code, name=stop.name, buses=[buses[0]])
      return updateStopList(session.user.userId, newStop)
    else:
      session.attributes[STOPID_KEY] = stop.code
      session.attributes[STOPNAME_KEY] = stop.name

      return question(render_template("add_stop_question", buses=getSentence(buses), stop=StopID)).reprompt(
        render_template("add_stop_reprompt"))

@ask.intent("AddBus")
def addBus(BusID):
  if BusID is None or STOPID_KEY not in session.attributes or STOPNAME_KEY not in session.attributes:
    return statement(render_template("no_bus_stop"))

  BusID = BusID.upper()
  stop = Stop(TOKEN, session.attributes[STOPID_KEY], session.attributes[STOPID_KEY])
  departures = stop.all_departures()
  buses = list(set(d.route for d in departures))

  if BusID not in buses:
    return statement(render_template("bad_route", bus=BusID))
  else:
    newStop = dict(code=stop.code, name=session.attributes[STOPNAME_KEY], buses=[BusID])
    return updateStopList(session.user.userId, newStop)

@ask.intent("RemoveBus")
def removeBus(BusID):
    if BusID is None:
      return statement(render_template("remove_no_bus_id"))

    BusID = BusID.upper()
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))

    if isResponseEmpty(response):
      return statement(render_template("remove_no_buses"))

    stops = response["Items"][0]['stops']

    deleteStop = None
    updateDynamo = False

    log.debug("Initial stops {}".format(stops))

    for s in stops:
      if "buses" in stops[s] and BusID in stops[s]["buses"]:
        log.debug("Deleting {} from stop {}".format(BusID, s))
        stops[s]["buses"].remove(BusID)
        log.debug("Deleted {}? {}".format(BusID, stops[s]["buses"]))
        updateDynamo = True
        if len(stops[s]["buses"]) == 0:
          deleteStop = s
        break

    if deleteStop:
      stops.pop(deleteStop, None)

    if updateDynamo:
      log.debug("Updating dynamo with {}".format(stops))
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
      responseText = render_template("remove_success", bus=BusID)
      return statement(responseText).simple_card(card_title, responseText)
    else:
      return statement(render_template("remove_no_bus_in_list", bus=BusID))

@ask.intent("ListBuses")
def listBuses():
    response = dynamodb_table.query(KeyConditionExpression=Key('userId').eq(session.user.userId))
    if isResponseEmpty(response):
      return statement(render_template("no_bus_stop"))

    stops = response["Items"][0]['stops']
    buses = []
    for s in stops:
      if 'buses' in stops[s] and len(stops[s]) > 0:
        buses += stops[s]["buses"]

    card_title = render_template('card_title')
    responseText = render_template("list_buses", buses=getSentence(buses))
    return statement(responseText).simple_card(card_title, responseText)

@ask.session_ended
def session_ended():
    return "", 200

if __name__ == '__main__':
    if not TOKEN:
      raise Exception("Set the FIVEONEONE_TOKEN env var before launching")
    app.run(debug=True)