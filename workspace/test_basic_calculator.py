

def run_tests():
    assert add(1, 2) == 3, 'Add test failed'
    assert subtract(5, 3) == 2, 'Subtract test failed'
    assert multiply(3, 4) == 12, 'Multiply test failed'
    assert divide(10, 2) == 5, 'Divide test failed'
    try:
        divide(5, 0)
    except ValueError:
        pass  # Expected behavior
    else:
        assert False, 'Divide by zero test failed'
    assert exponentiate(2, 3) == 8, 'Exponentiate test failed'
    assert exponentiate(5, 0) == 1, 'Exponentiate zero exponent test failed'
    assert exponentiate(2, 4) == 16, 'Exponentiate test for 2^4 failed'
    print('All tests passed!')

    assert add(1, 2) == 3, 'Add test failed'
    assert subtract(5, 3) == 2, 'Subtract test failed'
    assert multiply(3, 4) == 12, 'Multiply test failed'
    assert divide(10, 2) == 5, 'Divide test failed'
    try:
        divide(5, 0)
    except ValueError:
        pass  # Expected behavior
    else:
        assert False, 'Divide by zero test failed'
    assert exponentiate(2, 3) == 8, 'Exponentiate test failed'
    assert exponentiate(5, 0) == 1, 'Exponentiate zero exponent test failed'
    print('All tests passed!')

if __name__ == '__main__':
    run_tests()

import unittest
from calculator import exponentiate

class TestCalculator(unittest.TestCase):
    def test_exponentiate(self):
        self.assertEqual(exponentiate(2, 3), 8)
        self.assertEqual(exponentiate(2, 0), 1)
        self.assertEqual(exponentiate(5, 2), 25)

if __name__ == '__main__':
    unittest.main()  
