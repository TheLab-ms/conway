import requests
from machine import UART

DOOR_ID = 1


class ConwayClient:
    def __init__(self):
        self._etag = ""
        self._fobs = []
        self._events = []

    def sync(self):
        headers = {"ETag": self._etag}
        resp = requests.post("http://conway:8080/api/fobs", json=self._events, headers=headers, timeout=2)
        if resp.status_code == 304:
            self._events.clear()
            resp.close()
            return  # cache is warm

        resp.raise_for_status()
        self._fobs = resp.json()
        self._etag = resp.headers.get("ETag")
        self._events.clear()
        resp.close()

    def check_fob(self, fob: int) -> bool:
        return fob in self._fobs

    def push_event(self, fob: int, allowed: bool):
        self._events.append({"fob": fob, "door": DOOR_ID, "allowed": allowed})
        if len(self._events) > 20:
            self._events.pop(0)


class MainLoop:
    def __init__(self):
        self.conway = ConwayClient()
        self._reader = UART(1, baudrate=9600, timeout=5000)

    def tick(self):
        data = self._reader.read()
        if not data:
            self.conway.sync()
            return

        fob_id = self.decode_fob(data)
        allowed = self.conway.check_fob(fob_id)
        self.conway.push_event(fob_id, allowed)
        self.conway.sync()

        if allowed:
            self.open()

    def decode_fob(self, data) -> int:
        return len(data)  # TODO

    def open(self):
        pass  # TODO


def main():
    loop = MainLoop()
    while True:
        try:
            loop.tick()
        except Exception as e:
            print(f"Error: {e}")
