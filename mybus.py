#!/usr/bin/env python

"""
Alexa skill that queries the 511.org service for incoming bus times in SF
"""

import logging
import os

from aws_xray_sdk.ext.flask.middleware import XRayMiddleware
from aws_xray_sdk.core import patcher, xray_recorder

import boto3
from boto3.dynamodb.conditions import Key
from fiveoneone.stop import Stop
from flask import Flask, render_template
from flask_ask import Ask, question, session, statement


TOKEN = os.getenv("FIVEONEONE_TOKEN")
DYNAMO_ENDPOINT = os.getenv("DYNAMO_ENDPOINT", None)
DYNAMO_REGION = os.getenv("DYNAMO_REGION", "us-east-1")
LOGLEVEL = os.getenv("LOGLEVEL", "INFO")
STOPID_KEY = "stopid"
STOPNAME_KEY = "stopname"
BUSES_KEY = "buses"


# Patch the requests module to enable automatic instrumentation
patcher.patch(('requests',))

# pylint: disable=C0103
app = Flask(__name__)
app.config["ASK_APPLICATION_ID"] = os.getenv("APPLICATION_ID")

# pylint: disable=C0103
ask = Ask(app, "/")

logging.getLogger("flask_ask").setLevel(LOGLEVEL)
logging.getLogger(__name__).setLevel(LOGLEVEL)
log = logging.getLogger(__name__)

# Configure the X-Ray recorder to generate segments with our service name
xray_recorder.configure(service='mybus')

# Instrument the Flask application
XRayMiddleware(app, xray_recorder)


if DYNAMO_ENDPOINT:
    dynamodb = boto3.resource(
        'dynamodb', region_name=DYNAMO_REGION, endpoint_url=DYNAMO_ENDPOINT)
else:
    dynamodb = boto3.resource('dynamodb', region_name=DYNAMO_REGION)

dynamodb_table = dynamodb.Table("sfbus")


def isResponseEmpty(response):
    """
    Checks if the response from dynamodb doesn't contain any stops
    """
    if not response or len(response["Items"]) == 0 or "stops" not in response["Items"][0] or \
            len(response["Items"][0]["stops"]) == 0:
        return True

    return False


def askToAddAStop():
    """
    Starts the flow that asks the invoker to add a bus stop and bus number
    """
    return question(render_template("no_bus_stop")).reprompt(
        render_template("no_bus_stop_reprompt"))


def updateStopList(userId, newStop):
    """
    Updates the list of stops for the user in the dynamodb table
    """
    response = dynamodb_table.query(
        KeyConditionExpression=Key('userId').eq(userId))

    if response and len(response["Items"]) > 0:
        stops = response["Items"][0]['stops']
    else:
        stops = {}

    if newStop['code'] in stops:
        existingStop = stops[newStop['code']]
        if 'buses' in existingStop:
            newStop['buses'] = list(
                set(existingStop['buses'] + newStop['buses']))

    stops[newStop['code']] = newStop

    response = dynamodb_table.update_item(
        Key={
            'userId': userId
        },
        UpdateExpression="set stops = :s",
        ExpressionAttributeValues={
            ':s': stops
        }
    )

    card_title = render_template('card_title')
    responseText = render_template(
        "add_bus_success", stop=newStop['code'], route=",".join(newStop['buses']))
    return statement(responseText).simple_card(card_title, responseText)


def getSentence(words):
    """
    Forms a sentence from a list of words. If more than one element, the last one will have an 'and'
    """
    if len(words) == 0:
        return ""
    elif len(words) == 1:
        return words[0]

    return "{} and {}".format(", ".join([str(w) for w in words[:-1]]), str(words[-1]))


@ask.launch
@ask.intent("GetMyBus")
def getBusTimes():
    """
    Returns the incoming times of the stops configured for the user. If not configured,
    it prompts the user to add a stop.
    """
    card_title = render_template('card_title')

    response = dynamodb_table.query(
        KeyConditionExpression=Key('userId').eq(session.user.userId))
    if isResponseEmpty(response):
        return askToAddAStop()
    else:
        departures = []

        for key in response["Items"][0]['stops']:
            s = response["Items"][0]['stops'][key]
            stop = Stop(TOKEN, s["name"], s["code"])
            if "buses" in s:
                for r in s["buses"]:
                    try:
                        d = stop.next_departures(r)
                        readable_departure_times = getSentence(d.times)
                        departures.append(
                            dict(bus=d.route, departures=readable_departure_times))
                    except:
                        log.exception("Failed to get departures for %s", r)

        if len(departures) == 0:
            return statement(render_template("get_departures_failed"))
        else:
            responseStatement = render_template(
                "get_departures_success", departures=departures)

            return statement(responseStatement).simple_card(card_title, responseStatement)


@ask.intent("AddStop")
def addStop(StopID):
    """
    Adds a stop to the list of stops for the user invoking the skill
    """
    if StopID is None:
        return question(render_template("no_stop_id")).reprompt(
            render_template("no_stop_id_reprompt"))

    stop = Stop(TOKEN, StopID, StopID)

    try:
        stop.load()
    except:
        log.exception("error loading the stop")
        return question(render_template("add_stop_question", stop=StopID)).reprompt(
            render_template("add_stop_reprompt"))

    departures = stop.all_departures()
    buses = list(set(d.route for d in departures))

    if len(buses) == 1:
        newStop = dict(code=stop.code, name=stop.name, buses=[buses[0]])
        return updateStopList(session.user.userId, newStop)
    else:
        session.attributes[STOPID_KEY] = stop.code
        session.attributes[STOPNAME_KEY] = stop.name
        session.attributes[BUSES_KEY] = ",".join(buses)

        return question(
            render_template("add_bus_question", buses=getSentence(buses), stop=StopID, bus=buses[0])).reprompt(
                render_template("add_bus_reprompt"))


@ask.intent("AddBus")
def addBus(BusID):
    """
    Adds a bus to the list of stops for the user invoking the skill
    """
    if STOPID_KEY not in session.attributes or \
            STOPNAME_KEY not in session.attributes or \
            BUSES_KEY not in session.attributes:
        return askToAddAStop()

    buses = session.attributes[BUSES_KEY].split(",")

    if BusID is None:
        return question(render_template("no_bus_id", bus=buses[0])).reprompt(
            "no_bus_id_reprompt", bus=buses[0])

    BusID = BusID.upper()
    stop = Stop(
        TOKEN, session.attributes[STOPID_KEY], session.attributes[STOPID_KEY])

    if BusID not in buses:
        return question(render_template("bad_route", bus=BusID)).reprompt(
            render_template("bad_route", bus=BusID))
    else:
        newStop = dict(
            code=stop.code, name=session.attributes[STOPNAME_KEY], buses=[BusID])
        return updateStopList(session.user.userId, newStop)


@ask.intent("RemoveBus")
def removeBus(BusID):
    """
    Removes the bus from the list of buses for the user invoking the skill
    """
    if BusID is None:
        return question(render_template("remove_no_bus_id")).reprompt(
            render_template("remove_no_bus_id_reprompt"))

    BusID = BusID.upper()
    response = dynamodb_table.query(
        KeyConditionExpression=Key('userId').eq(session.user.userId))

    if isResponseEmpty(response):
        return statement(render_template("remove_no_buses"))

    stops = response["Items"][0]['stops']

    deleteStop = None
    updateDynamo = False

    log.debug("Initial stops %s", stops)

    for s in stops:
        if "buses" in stops[s] and BusID in stops[s]["buses"]:
            log.debug("Deleting %s from stop %s", BusID, s)
            stops[s]["buses"].remove(BusID)
            log.debug("Deleted %s? %s", BusID, stops[s]["buses"])
            updateDynamo = True
            if len(stops[s]["buses"]) == 0:
                deleteStop = s
            break

    if deleteStop:
        stops.pop(deleteStop, None)

    if updateDynamo:
        log.debug("Updating dynamo with %s", stops)
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
    """
    Lists all the buses configured for  the user invoking the skill
    """
    response = dynamodb_table.query(
        KeyConditionExpression=Key('userId').eq(session.user.userId))

    if isResponseEmpty(response):
        return askToAddAStop()

    stops = response["Items"][0]['stops']
    buses = []
    for s in stops:
        if 'buses' in stops[s] and len(stops[s]) > 0:
            buses += stops[s]["buses"]

    card_title = render_template('card_title')
    responseText = render_template("list_buses", buses=getSentence(buses))
    return statement(responseText).simple_card(card_title, responseText)


@ask.intent("AMAZON.CancelIntent")
@ask.intent("AMAZON.StopIntent")
def cancel():
    """
    Cancels the session
    """
    return statement(render_template("goodbye"))


@ask.intent("AMAZON.HelpIntent")
def help():
    """
    Help
    """
    response = render_template("help")
    return question(response).reprompt(response)


@ask.session_ended
def session_ended():
    """
    Returns a 200 to mark that the session is over
    """
    return "", 200


if __name__ == '__main__':
    logging.info("Starting")
    if not TOKEN:
        raise Exception("Set the FIVEONEONE_TOKEN env var before launching")
    port = os.getenv("PORT", "5000")
    app.run(debug=True, port=port, host="0.0.0.0")
