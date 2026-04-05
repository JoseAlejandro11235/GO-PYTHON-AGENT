def add(x, y):
    return x + y

def subtract(x, y):
    return x - y

def multiply(x, y):
    return x * y

def divide(x, y):
    if y == 0:
        raise ValueError('Cannot divide by zero')
    return x / y

def exponentiate(base, exp):
    return base ** exp


def main():
    print('Basic Calculator')
    for _ in range(2):
        operation = input('Enter operation (+, -, *, /, pow) or q to quit: ')
        if operation == 'q':
            break
        if operation in ('+', '-', '*', '/', 'pow'):
            try:
                x = float(input('Enter first number: '))
                y = float(input('Enter second number: '))
            except ValueError:
                print('Invalid input. Please enter numbers.')
                continue
            if operation == '+':
                print(f'Result: {add(x, y)}')
            elif operation == '-':
                print(f'Result: {subtract(x, y)}')
            elif operation == '*':
                print(f'Result: {multiply(x, y)}')
            elif operation == 'pow':
                print(f'Result: {exponentiate(x, y)}')
                try:
                    print(f'Result: {divide(x, y)}')
                except ValueError as e:
                    print(e)
    else:
            print('Invalid operation. Please enter one of +, -, *, /.')

if __name__ == '__main__':
    main()
