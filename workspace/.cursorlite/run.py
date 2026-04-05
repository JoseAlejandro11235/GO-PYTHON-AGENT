from pathlib import Path

test_path = Path('test_basic_calculator.py')
test_contents = test_path.read_text(encoding='utf-8')

new_tests = """
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
"""

test_contents = test_contents.replace('def run_tests():', new_tests)

# Write the updated test contents back to the file
test_path.write_text(test_contents, encoding='utf-8', newline='\n')