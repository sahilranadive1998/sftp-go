import time
import argparse

def read_file_line_by_line(file_name):
    """Reads the file line by line and prints each line."""
    nfs_write_time = 0
    nfs_read_time = 0
    cache_write_time = 0
    cache_read_time = 0
    nfs_read_count = 0
    nfs_write_count = 0
    cache_write_count = 0
    try:
        with open(file_name, 'r') as file:
            for line in file:
                if "NFS" in line:
                    split_line = line.split(",")
                    if len(split_line) < 2:
                        continue
                    if "write" in split_line[1]:
                        t = split_line[1].split(":")[1]
                        nfs_write_time = nfs_write_time + int(t)
                        nfs_write_count += 1
                    elif "read" in split_line[1]:
                        t = split_line[1].split(":")[1]
                        nfs_read_time = nfs_read_time + int(t)
                        nfs_read_count += 1
                elif "Cache" in line:
                    split_line = line.split(",")
                    if "write" in split_line[1] or "overwrite" in split_line[1]:
                        t = split_line[1].split(":")[1]
                        cache_write_time = cache_write_time + int(t)
                        cache_write_count += 1
                    elif "read" in split_line[1]:
                        t = split_line[1].split(":")[1]
                        cache_read_time = cache_read_time + int(t)


                # print(line.strip())
    except FileNotFoundError:
        print(f"Error: The file '{file_name}' was not found.")
    except IOError:
        print(f"Error: Could not read the file '{file_name}'.")

    print("Cache read time: "+ str(cache_read_time))
    print("Cache write time: "+ str(cache_write_time))
    print("NFS read time: "+ str(nfs_read_time))
    print("NFS write time: "+ str(nfs_write_time))
    print("...")
    print("Cache write count: "+ str(cache_write_count))
    
    print("NFS write count: "+ str(nfs_write_count))
    print("NFS read count: "+ str(nfs_read_count))

if __name__ == "__main__":
    # Set up argument parsing
    parser = argparse.ArgumentParser(description="Process a file and read it line by line.")
    parser.add_argument("filename", type=str, help="The name of the file to read")
    args = parser.parse_args()

    # Read and print the file line by line
    read_file_line_by_line(args.filename)
