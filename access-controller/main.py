import requests
from machine import UART, Pin, Timer
import network
import time
import utime
import json

WIEGAND_D0_PIN = 14
WIEGAND_D1_PIN = 27
DOOR_PIN = 25


# TODO: On demand unlock
# TODO: Status LED


class ConwayClient:
    def __init__(self, url: str):
        self.url = url
        self._etag = ""
        self._fobs = []
        self._events = []

    def maybe_sync(self):
        try:
            self.sync()
        except Exception as e:
            print(f"error while attempting to sync with Conway: {e}")

    def sync(self):
        headers = {"If-None-Match": self._etag}
        resp = requests.post(f"{self.url}/api/fobs", json=self._events, headers=headers, timeout=2)

        if resp.status_code == 304:
            self._events.clear()
            resp.close()
            return  # cache is warm

        if resp.status_code != 200:
            resp.close()
            raise Exception(f"Unexpected server response status: {resp.status_code}")

        self._fobs = resp.json()
        self._etag = resp.headers.get("Etag")
        self._events.clear()
        resp.close()

    def check_fob(self, fob: int) -> bool:
        return fob in self._fobs

    def push_event(self, fob: int, allowed: bool):
        self._events.append({"fob": fob, "allowed": allowed})
        if len(self._events) > 20:
            self._events.pop(0)


class Wiegand:
    CARD_MASK = (1 << 17) - 2  # bits 1–16 (card number)
    FACILITY_MASK = 0xFF << 17  # bits 17–24 (facility code)
    DEBOUNCE_US = 200
    END_OF_TX_US = 25000

    def __init__(self, pin0, pin1, callback, timer_id=-1):
        self.callback = callback
        self.last_card = None
        self.bit_buffer = []
        self.last_bit_time = None
        self.cards_read = 0

        # configure pins
        self.pin0 = Pin(pin0, Pin.IN, Pin.PULL_UP)
        self.pin1 = Pin(pin1, Pin.IN, Pin.PULL_UP)
        self.pin0.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(0))
        self.pin1.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(1))

        # configure timer
        self.timer = Timer(timer_id)
        self.timer.init(period=5, mode=Timer.PERIODIC, callback=self._cardcheck)

    def _on_pin(self, bit_value):
        now = utime.ticks_us()
        if self.last_bit_time and utime.ticks_diff(now, self.last_bit_time) < Wiegand.DEBOUNCE_US:
            return  # ignore bounce
        self.last_bit_time = now
        self.bit_buffer.append(bit_value)

    def _cardcheck(self, *_):
        now = utime.ticks_us()
        if not self.last_bit_time or not self.bit_buffer or utime.ticks_diff(now, self.last_bit_time) < Wiegand.END_OF_TX_US:
            return

        value = int("".join(map(str, self.bit_buffer)), 2)
        if not self._check_parity(value):
            print("parity check failed!")
            self._reset()
            return

        self.last_card = value
        self.cards_read += 1
        card_id = (value & Wiegand.CARD_MASK) >> 1
        facility = (value & Wiegand.FACILITY_MASK) >> 17
        self.callback(card_id, facility, self.cards_read)
        self._reset()

    def _check_parity(self, value):
        leading, trailing = (value >> 25) & 1, value & 1
        data = (value >> 1) & ((1 << 24) - 1)
        return (bin(data >> 12).count("1") % 2) == leading and (bin(data & 0xFFF).count("1") % 2) != trailing

    def _reset(self):
        self.bit_buffer.clear()
        self.last_bit_time = None


class MainLoop:
    def __init__(self):
        with open("network.conf", "r") as f:
            config = json.load(f)
            sta_if = network.WLAN(network.WLAN.IF_STA)
            sta_if.active(True)
            sta_if.connect(config["ssid"], config["password"])
            sta_if.isconnected()
            sta_if.ipconfig("addr4")
            self.conway = ConwayClient(config["url"])

        self.door = Pin(DOOR_PIN, Pin.OUT)
        Wiegand(WIEGAND_D0_PIN, WIEGAND_D1_PIN, self._on_card, 1)

    def _on_card(self, card_number, facility_code, _):
        combined = int(f"{facility_code}{card_number}")
        print(f"saw card {combined}")

        allowed = self.conway.check_fob(combined)
        if not allowed:  # refresh the cache
            self.conway.maybe_sync()
            allowed = self.conway.check_fob(combined)

        self.conway.push_event(combined, allowed)
        if allowed:
            self.open_door()

    def open_door(self):
        print("opening door")
        self.door.on()
        time.sleep(0.2)
        self.door.off()


main = MainLoop()
