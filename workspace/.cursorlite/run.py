import os
import pathlib

# Specify the files to delete
files_to_delete = [pathlib.Path('tic_tac_toe.py')]

# Delete each specified file
for file in files_to_delete:
    if file.exists():
        file.unlink()