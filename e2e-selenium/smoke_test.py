"""Firefox smoke test for the public pages, driven by Selenium (WebDriver).

Run from this directory:
    BASE_URL=https://dev.balfolk.jetzt .venv/bin/python -m unittest -v smoke_test

Per page: the HTTP status is 200, the browser loads the page, the title
mentions Balfolk, and the page has a <main> element. Selenium Manager fetches
a matching geckodriver on first run. FIREFOX_BIN overrides the browser binary.
"""

import os
import unittest
import urllib.request

from selenium import webdriver
from selenium.common.exceptions import NoSuchElementException
from selenium.webdriver.common.by import By
from selenium.webdriver.firefox.options import Options
from selenium.webdriver.support.ui import WebDriverWait

BASE_URL = os.environ.get("BASE_URL", "https://dev.balfolk.jetzt").rstrip("/")
FIREFOX_BIN = os.environ.get("FIREFOX_BIN", "/usr/bin/firefox-esr")
PAGES = ["/", "/festivals", "/search"]
TIMEOUT = 20


def http_status(url: str) -> int:
    req = urllib.request.Request(url, headers={"User-Agent": "dansal-selenium-smoke"})
    with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
        return resp.status


class PublicPagesSmoke(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        opts = Options()
        opts.add_argument("-headless")
        opts.binary_location = FIREFOX_BIN
        cls.driver = webdriver.Firefox(options=opts)
        cls.driver.set_page_load_timeout(TIMEOUT)

    @classmethod
    def tearDownClass(cls):
        cls.driver.quit()

    def check_page(self, path: str):
        url = BASE_URL + path
        with self.subTest(page=path, check="http status"):
            self.assertEqual(http_status(url), 200)

        self.driver.get(url)
        with self.subTest(page=path, check="title"):
            self.assertIn("Balfolk", self.driver.title)

        with self.subTest(page=path, check="main element"):
            WebDriverWait(self.driver, TIMEOUT).until(
                lambda d: d.execute_script("return document.readyState") == "complete"
            )
            try:
                self.driver.find_element(By.TAG_NAME, "main")
            except NoSuchElementException:
                self.fail(f"{path}: no <main> element")

    def test_home(self):
        self.check_page("/")

    def test_festivals(self):
        self.check_page("/festivals")

    def test_search(self):
        self.check_page("/search")


if __name__ == "__main__":
    unittest.main()
