
import pytest
from calculator import add, subtract, multiply, divide, exponentiate


def test_add():
    assert add(1, 2) == 3
    assert add(-1, 1) == 0


def test_subtract():
    assert subtract(5, 3) == 2
    assert subtract(-1, -1) == 0


def test_multiply():
    assert multiply(3, 4) == 12
    assert multiply(0, 5) == 0


def test_divide():
    assert divide(10, 2) == 5
    with pytest.raises(ValueError):
        divide(5, 0)


def test_exponentiate():
    assert exponentiate(2, 3) == 8
    assert exponentiate(5, 0) == 1


import unittest
from calculator import exponentiate

class TestCalculator(unittest.TestCase):
    def test_exponentiation(self):
        self.assertEqual(exponentiate(2, 3), 8)
        self.assertEqual(exponentiate(-2, 3), -8)
        self.assertEqual(exponentiate(2, 0), 1)

if __name__ == '__main__':
    unittest.main()
